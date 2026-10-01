package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/gin-gonic/gin"

	"github.com/IwatsukaYura/kin-openai-sample/api"
	"github.com/IwatsukaYura/kin-openai-sample/internal/handler"
	"github.com/IwatsukaYura/kin-openai-sample/internal/server"
	"github.com/IwatsukaYura/kin-openai-sample/internal/store"
)

const apiKey = "test-key"

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

func TestAPI(t *testing.T) {
	r, err := server.New(server.Config{APIKey: apiKey}, handler.New(store.NewMemory()))
	if err != nil {
		t.Fatal(err)
	}
	specRouter := newSpecRouter(t)

	tests := []struct {
		name       string
		method     string
		path       string
		apiKey     string
		body       string
		wantStatus int
		// wantMessages はエラーメッセージに含まれるべき文字列
		wantMessages []string
	}{
		{name: "正常: 作成", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"name":"taro","email":"taro@example.com","role":"admin","age":20,"tags":["go"]}`, wantStatus: 201},
		{name: "正常: 一覧", method: http.MethodGet, path: "/users?limit=10&role=admin", wantStatus: 200},
		{name: "正常: 取得", method: http.MethodGet, path: "/users/1", wantStatus: 200},
		{name: "業務ルール: メールアドレス重複はハンドラーで 409", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"name":"taro2","email":"taro@example.com","role":"member"}`, wantStatus: 409},
		{name: "存在しないユーザー", method: http.MethodGet, path: "/users/999", wantStatus: 404},
		{name: "OpenAPI 定義外 (healthz)", method: http.MethodGet, path: "/healthz", wantStatus: 204},

		{name: "未定義のパス", method: http.MethodGet, path: "/unknown", wantStatus: 404},
		{name: "未定義のメソッド", method: http.MethodDelete, path: "/users", wantStatus: 405},

		{name: "path: 型違い", method: http.MethodGet, path: "/users/abc", wantStatus: 400,
			wantMessages: []string{`parameter "id" in path`, "invalid integer"}},
		{name: "path: minimum", method: http.MethodGet, path: "/users/0", wantStatus: 400,
			wantMessages: []string{`parameter "id" in path`, "at least 1"}},
		{name: "query: maximum と enum (MultiError で全件返る)", method: http.MethodGet, path: "/users?limit=101&role=guest", wantStatus: 400,
			wantMessages: []string{`parameter "limit" in query`, "at most 100", `parameter "role" in query`, "allowed values"}},

		{name: "security: API キーなし", method: http.MethodPost, path: "/users",
			body: `{"name":"taro","email":"taro@example.com","role":"admin"}`, wantStatus: 401},
		{name: "security: API キー違い", method: http.MethodPost, path: "/users", apiKey: "wrong",
			body: `{"name":"taro","email":"taro@example.com","role":"admin"}`, wantStatus: 401},

		{name: "body: 必須プロパティ欠落", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"email":"taro@example.com"}`, wantStatus: 400,
			wantMessages: []string{`property "name" is missing`, `property "role" is missing`}},
		{name: "body: 型違い", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"name":1,"email":"taro@example.com","role":"admin"}`, wantStatus: 400,
			wantMessages: []string{`"/name": value must be a string`}},
		{name: "body: 各種制約違反", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"name":"","email":"bad","role":"guest","age":151,"tags":["Go","a","a","b"],"extra":1}`, wantStatus: 400,
			wantMessages: []string{
				`"/name": minimum string length`,
				`"/email": string doesn't match the format "email"`,
				`"/role": value is not one of the allowed values`,
				`"/age": number must be at most 150`,
				`"/tags": maximum number of items`,
				`"/tags": duplicate items`,
				`"/tags/0": string doesn't match the regular expression`,
				`property "extra" is unsupported`, // additionalProperties: false
			}},
		{name: "body: maxLength", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{"name":"` + strings.Repeat("a", 21) + `","email":"taro@example.com","role":"admin"}`, wantStatus: 400,
			wantMessages: []string{`"/name": maximum string length`}},
		{name: "body: 壊れた JSON", method: http.MethodPost, path: "/users", apiKey: apiKey,
			body: `{broken`, wantStatus: 400, wantMessages: []string{"failed to decode request body"}},
		{name: "body: 空", method: http.MethodPost, path: "/users", apiKey: apiKey,
			wantStatus: 400, wantMessages: []string{"value is required"}},
	}

	// 先頭のケースで作ったユーザーを後続で参照するので順番に実行する
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if strings.HasPrefix(tt.path, "/users") {
				assertResponseConformsToSpec(t, specRouter, tt.method, tt.path, tt.body, rec)
			}
			if len(tt.wantMessages) == 0 {
				return
			}
			var got api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.wantMessages {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q does not contain %q", got.Message, want)
				}
			}
		})
	}
}

func newSpecRouter(t *testing.T) routers.Router {
	t.Helper()
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	r, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// assertResponseConformsToSpec は、レスポンスが OpenAPI 定義に沿っているかを kin-openapi で検証する。
// 本番でレスポンスを検証するとコストが高いので、テストでだけ行う。
func assertResponseConformsToSpec(t *testing.T, r routers.Router, method, path, body string, rec *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	route, pathParams, err := r.FindRoute(req)
	if err != nil {
		return // 定義外のパス / メソッド
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
		},
		Status: rec.Code,
		Header: rec.Header(),
		Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{
			// 定義していないステータスコードを返したらエラーにする
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Errorf("response does not conform to openapi spec: %v\nbody: %s", err, rec.Body.String())
	}
}
