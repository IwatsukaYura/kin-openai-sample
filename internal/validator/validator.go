// Package validator は kin-openapi を使って、OpenAPI 定義に基づく
// リクエスト / レスポンスのバリデーションを行う net/http ミドルウェアを提供する。
//
// oapi-codegen の nethttp-middleware も内部的には同じことをしているが、
// ここでは kin-openapi の API (openapi3filter / routers) を直接使って
// 何が起きているのかを見えるようにしている。
package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// APIKey は X-API-Key ヘッダーで受け付けるサンプル用の固定キー。
const APIKey = "secret"

func init() {
	// kin-openapi は "email" フォーマットをデフォルトでは検証しない
	// (date / date-time / byte / int32 / int64 のみ登録済み)。
	// format: email を効かせたい場合は自分で登録する必要がある。
	openapi3.DefineStringFormatValidator("email",
		openapi3.NewRegexpFormatValidator(`^[^@\s]+@[^@\s]+\.[^@\s]+$`))
}

// ErrorDetail はバリデーションエラー 1 件分。
type ErrorDetail struct {
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason"`
}

// ErrorResponse は OpenAPI の Error スキーマと同じ形のレスポンス。
type ErrorResponse struct {
	Message string        `json:"message"`
	Errors  []ErrorDetail `json:"errors,omitempty"`
}

// Config はミドルウェアの設定。
type Config struct {
	// ValidateResponse を true にすると、ハンドラーが返したレスポンスも
	// OpenAPI 定義に沿っているか検証する (開発・テスト向け)。
	ValidateResponse bool
}

// Middleware は OpenAPI 定義に基づいてリクエストを検証する。
type Middleware struct {
	router routers.Router
	cfg    Config
}

// New は spec からルーターを作り、ミドルウェアを生成する。
func New(spec *openapi3.T, cfg Config) (*Middleware, error) {
	// servers に URL が書かれていると Host ヘッダーまで照合されてしまうので外す。
	spec.Servers = nil

	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid openapi spec: %w", err)
	}
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	return &Middleware{router: router, cfg: cfg}, nil
}

// requestOptions は openapi3filter.ValidateRequest に渡すオプション。
func requestOptions() *openapi3filter.Options {
	return &openapi3filter.Options{
		// true にすると最初のエラーで止まらず、全てのエラーを openapi3.MultiError で返す。
		MultiError: true,
		// security が定義されたオペレーションでは AuthenticationFunc が必須。
		// 未設定だと全リクエストが "security requirements failed" になる。
		AuthenticationFunc: authenticate,
	}
}

func authenticate(_ context.Context, input *openapi3filter.AuthenticationInput) error {
	if input.SecuritySchemeName != "ApiKeyAuth" {
		return fmt.Errorf("unsupported security scheme %q", input.SecuritySchemeName)
	}
	if input.RequestValidationInput.Request.Header.Get(input.SecurityScheme.Name) != APIKey {
		return errors.New("invalid api key")
	}
	return nil
}

// Handler は next をラップして、リクエスト (と任意でレスポンス) を検証する。
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. リクエストに対応するオペレーションを OpenAPI 定義から探す
		route, pathParams, err := m.router.FindRoute(r)
		if err != nil {
			status := http.StatusNotFound
			if errors.Is(err, routers.ErrMethodNotAllowed) {
				status = http.StatusMethodNotAllowed
			}
			writeJSON(w, status, ErrorResponse{Message: err.Error()})
			return
		}

		// 2. パラメーター / ボディ / セキュリティを検証
		reqInput := &openapi3filter.RequestValidationInput{
			Request:    r,
			PathParams: pathParams,
			Route:      route,
			Options:    requestOptions(),
		}
		if err := openapi3filter.ValidateRequest(r.Context(), reqInput); err != nil {
			status, resp := toErrorResponse(err)
			writeJSON(w, status, resp)
			return
		}

		if !m.cfg.ValidateResponse {
			next.ServeHTTP(w, r)
			return
		}

		// 3. レスポンスを一旦バッファに書かせてから検証する
		rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		resInput := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: reqInput,
			Status:                 rec.status,
			Header:                 rec.header,
			Options: &openapi3filter.Options{
				MultiError: true,
				// 定義されていないステータスコードを返したらエラーにする
				IncludeResponseStatus: true,
			},
		}
		resInput.SetBodyBytes(rec.body.Bytes())
		if err := openapi3filter.ValidateResponse(r.Context(), resInput); err != nil {
			slog.Error("response does not conform to openapi spec", "error", err)
			_, resp := toErrorResponse(err)
			resp.Message = "response validation failed"
			writeJSON(w, http.StatusInternalServerError, resp)
			return
		}

		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}

// toErrorResponse は kin-openapi のエラーを API のエラーレスポンスに変換する。
//
// kin-openapi が返すエラーの主な型:
//   - openapi3.MultiError                  : MultiError: true のときの複数エラー
//   - *openapi3filter.RequestError         : パラメーター / ボディのエラー
//   - *openapi3filter.SecurityRequirementsError : 認証エラー
//   - *openapi3filter.ResponseError        : レスポンスのエラー
//   - *openapi3.SchemaError                : スキーマ違反の詳細 (上記の Err に入っている)
func toErrorResponse(err error) (int, ErrorResponse) {
	resp := ErrorResponse{Message: "request validation failed"}
	status := http.StatusBadRequest

	// openapi3.MultiError は As メソッドを持っており、errors.As で判定すると
	// RequestError の中身の MultiError に先にマッチしてしまう。
	// そのため外側から順に type switch で剥がしていく。
	var walk func(err error, field string)
	walk = func(err error, field string) {
		switch e := err.(type) {
		case openapi3.MultiError:
			for _, inner := range e {
				walk(inner, field)
			}
		case *openapi3filter.SecurityRequirementsError:
			status = http.StatusUnauthorized
			resp.Message = "unauthorized"
			for _, inner := range e.Errors {
				resp.Errors = append(resp.Errors, ErrorDetail{Reason: inner.Error()})
			}
		case *openapi3filter.RequestError:
			f := field
			if e.Parameter != nil {
				f = e.Parameter.In + "." + e.Parameter.Name
			} else if e.RequestBody != nil {
				f = "body"
			}
			if e.Err == nil {
				resp.Errors = append(resp.Errors, ErrorDetail{Field: f, Reason: e.Reason})
				return
			}
			walk(e.Err, f)
		case *openapi3filter.ResponseError:
			if e.Err == nil {
				resp.Errors = append(resp.Errors, ErrorDetail{Field: "response", Reason: e.Reason})
				return
			}
			walk(e.Err, "response")
		case *openapi3.SchemaError:
			f := field
			if ptr := e.JSONPointer(); len(ptr) > 0 {
				f = field + "." + strings.Join(ptr, ".")
			}
			resp.Errors = append(resp.Errors, ErrorDetail{Field: f, Reason: e.Reason})
		default:
			resp.Errors = append(resp.Errors, ErrorDetail{Field: field, Reason: cleanReason(err.Error())})
		}
	}
	walk(err, "")
	return status, resp
}

var spaces = regexp.MustCompile(`\s+`)

func cleanReason(s string) string { return spaces.ReplaceAllString(s, " ") }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) WriteHeader(status int)      { r.status = status }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
