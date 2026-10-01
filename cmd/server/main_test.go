package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IwatsukaYura/kin-openai-sample/internal/validator"
)

type errorBody struct {
	Message string                  `json:"message"`
	Errors  []validator.ErrorDetail `json:"errors"`
}

func TestRequestValidation(t *testing.T) {
	h, err := NewHandler(true)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		apiKey     string
		body       string
		wantStatus int
		// wantErrors は "field: reason の一部" の形で、レスポンスに含まれるべきエラー
		wantErrors []string
	}{
		{name: "正常: 作成", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{"name":"taro","email":"taro@example.com","role":"admin","age":20,"tags":["go"]}`, wantStatus: 201},
		{name: "正常: 一覧", method: http.MethodGet, path: "/users?limit=10&role=admin", wantStatus: 200},
		{name: "正常: 取得", method: http.MethodGet, path: "/users/1", wantStatus: 200},
		{name: "ハンドラーの 404 は定義済みなので通る", method: http.MethodGet, path: "/users/999", wantStatus: 404},

		{name: "未定義のパス", method: http.MethodGet, path: "/unknown", wantStatus: 404},
		{name: "未定義のメソッド", method: http.MethodDelete, path: "/users", wantStatus: 405},

		{name: "path: 型違い", method: http.MethodGet, path: "/users/abc", wantStatus: 400,
			wantErrors: []string{"path.id: invalid integer"}},
		{name: "path: minimum", method: http.MethodGet, path: "/users/0", wantStatus: 400,
			wantErrors: []string{"path.id: at least 1"}},
		{name: "query: maximum と enum (MultiError で全件返る)", method: http.MethodGet, path: "/users?limit=101&role=guest", wantStatus: 400,
			wantErrors: []string{"query.limit: at most 100", "query.role: allowed values"}},

		{name: "security: API キーなし", method: http.MethodPost, path: "/users",
			body: `{"name":"taro","email":"taro@example.com","role":"admin"}`, wantStatus: 401},
		{name: "security: API キー違い", method: http.MethodPost, path: "/users", apiKey: "wrong",
			body: `{"name":"taro","email":"taro@example.com","role":"admin"}`, wantStatus: 401},

		{name: "body: 必須プロパティ欠落", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{"email":"taro@example.com"}`, wantStatus: 400,
			wantErrors: []string{`body.name: "name" is missing`, `body.role: "role" is missing`}},
		{name: "body: 型違い", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{"name":1,"email":"taro@example.com","role":"admin"}`, wantStatus: 400,
			wantErrors: []string{"body.name: must be a string"}},
		{name: "body: 各種制約違反", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{"name":"","email":"bad","role":"guest","age":151,"tags":["Go","a","a","b"],"extra":1}`, wantStatus: 400,
			wantErrors: []string{
				"body.name: minimum string length",
				`body.email: format "email"`,
				"body.role: allowed values",
				"body.age: at most 150",
				"body.tags: maximum number of items",
				"body.tags: duplicate items",
				"body.tags.0: regular expression",
				`body: "extra" is unsupported`, // additionalProperties: false
			}},
		{name: "body: maxLength", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{"name":"` + strings.Repeat("a", 21) + `","email":"taro@example.com","role":"admin"}`, wantStatus: 400,
			wantErrors: []string{"body.name: maximum string length"}},
		{name: "body: 壊れた JSON", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			body: `{broken`, wantStatus: 400, wantErrors: []string{"body: invalid character"}},
		{name: "body: 空", method: http.MethodPost, path: "/users", apiKey: validator.APIKey,
			wantStatus: 400, wantErrors: []string{"body: value is required"}},
	}

	// 先頭のケースで作ったユーザーを後続で参照するので順番に実行する
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
				if tt.method == http.MethodPost {
					req.Header.Set("Content-Type", "application/json")
				}
			}
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if len(tt.wantErrors) == 0 {
				return
			}
			var got errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.wantErrors {
				field, reason, _ := strings.Cut(want, ": ")
				if !containsError(got.Errors, field, reason) {
					t.Errorf("error %q not found in %s", want, rec.Body.String())
				}
			}
		})
	}
}

func containsError(errs []validator.ErrorDetail, field, reason string) bool {
	for _, e := range errs {
		if e.Field == field && strings.Contains(e.Reason, reason) {
			return true
		}
	}
	return false
}
