package validator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IwatsukaYura/kin-openai-sample/api"
	"github.com/IwatsukaYura/kin-openai-sample/internal/validator"
)

// TestResponseValidation は、ハンドラーが OpenAPI 定義に沿わないレスポンスを
// 返したときに kin-openapi がそれを検出できることを確認する。
func TestResponseValidation(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		validate   bool
		wantStatus int
		wantReason string
	}{
		{name: "定義どおり", status: 200, body: `{"id":1,"name":"a","email":"a@example.com","role":"admin"}`,
			validate: true, wantStatus: 200},
		{name: "必須フィールド欠落", status: 200, body: `{"id":1,"name":"a","role":"admin"}`,
			validate: true, wantStatus: 500, wantReason: `"email" is missing`},
		{name: "enum 違反", status: 200, body: `{"id":1,"name":"a","email":"a@example.com","role":"root"}`,
			validate: true, wantStatus: 500, wantReason: "allowed values"},
		{name: "未定義のステータスコード", status: 418, body: `{}`,
			validate: true, wantStatus: 500, wantReason: "status is not supported"},
		{name: "レスポンス検証オフならそのまま返る", status: 200, body: `{"id":1}`,
			validate: false, wantStatus: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := api.GetSpec()
			if err != nil {
				t.Fatal(err)
			}
			m, err := validator.New(spec, validator.Config{ValidateResponse: tt.validate})
			if err != nil {
				t.Fatal(err)
			}
			// わざと任意のレスポンスを返すハンドラー
			h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/1", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantReason == "" {
				return
			}
			var got validator.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, e := range got.Errors {
				if strings.Contains(e.Reason, tt.wantReason) {
					return
				}
			}
			t.Errorf("reason %q not found in %s", tt.wantReason, rec.Body.String())
		})
	}
}
