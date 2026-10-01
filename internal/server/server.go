// Package server は Gin のルーターを組み立てる。
//
// リクエストのバリデーションは oapi-codegen 公式の gin-middleware
// (内部で kin-openapi の openapi3filter.ValidateRequest を呼ぶ) に任せる。
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/gin-gonic/gin"
	ginmiddleware "github.com/oapi-codegen/gin-middleware"

	"github.com/IwatsukaYura/kin-openai-sample/api"
)

// authFailedKey は認証失敗を AuthenticationFunc から ErrorHandler に伝えるための gin.Context のキー。
const authFailedKey = "auth_failed"

func init() {
	// kin-openapi は "email" フォーマットをデフォルトでは検証しないため登録する。
	// (デフォルトで検証されるのは date / date-time / byte / int32 / int64 のみ)
	openapi3.DefineStringFormatValidator("email", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForEmail))

	// エラーメッセージにスキーマ定義や入力値全体がダンプされないようにする。
	// (クライアントに返すメッセージが冗長になり、内部情報も漏れるため)
	openapi3.SchemaErrorDetailsDisabled = true
}

type Config struct {
	// APIKey は X-API-Key ヘッダーで受け付けるキー。
	APIKey string
	Logger *slog.Logger
}

// New は OpenAPI 定義に基づくバリデーション付きの Gin エンジンを返す。
func New(cfg Config, h api.StrictServerInterface) (*gin.Engine, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("api key is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load openapi spec: %w", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid openapi spec: %w", err)
	}
	// servers が定義されていると Host ヘッダーまで照合されてしまうため外す。
	spec.Servers = nil

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.HandleMethodNotAllowed = true
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, api.Error{Message: "not found"})
	})
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, api.Error{Message: "method not allowed"})
	})

	// OpenAPI 定義外のエンドポイントはバリデーターを通さないグループの外に置く。
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// バリデーターは生成コードのパラメーターバインドより前に動かしたいので、
	// GinServerOptions.Middlewares ではなくルーターグループに付ける。
	apiGroup := r.Group("", ginmiddleware.OapiRequestValidatorWithOptions(spec, &ginmiddleware.Options{
		Options: openapi3filter.Options{
			// 最初の 1 件で止めず、全てのエラーを返す
			MultiError: true,
			// security が定義されたオペレーションでは必須。未設定だと常に失敗する
			AuthenticationFunc: newAuthenticationFunc(cfg.APIKey),
		},
		// デフォルトだと "multiple errors encountered: " が前に付くので、MultiError の文言をそのまま使う
		MultiErrorHandler: func(me openapi3.MultiError) error { return me },
		ErrorHandler: func(c *gin.Context, message string, statusCode int) {
			if c.GetBool(authFailedKey) {
				statusCode = http.StatusUnauthorized
				message = "unauthorized"
			}
			c.AbortWithStatusJSON(statusCode, api.Error{Message: message})
		},
	}))

	api.RegisterHandlersWithOptions(apiGroup, api.NewStrictHandlerWithOptions(h, nil, api.StrictGinServerOptions{
		RequestErrorHandlerFunc: func(c *gin.Context, err error) {
			c.JSON(http.StatusBadRequest, api.Error{Message: err.Error()})
		},
		HandlerErrorFunc: func(c *gin.Context, err error) {
			logger.ErrorContext(c, "handler error", "error", err, "path", c.FullPath())
			c.JSON(http.StatusInternalServerError, api.Error{Message: "internal server error"})
		},
		ResponseErrorHandlerFunc: func(c *gin.Context, err error) {
			logger.ErrorContext(c, "response error", "error", err, "path", c.FullPath())
			c.JSON(http.StatusInternalServerError, api.Error{Message: "internal server error"})
		},
	}), api.GinServerOptions{
		ErrorHandler: func(c *gin.Context, err error, statusCode int) {
			c.JSON(statusCode, api.Error{Message: err.Error()})
		},
	})

	return r, nil
}

func newAuthenticationFunc(apiKey string) openapi3filter.AuthenticationFunc {
	return func(ctx context.Context, input *openapi3filter.AuthenticationInput) error {
		if input.SecuritySchemeName != "ApiKeyAuth" {
			return fmt.Errorf("unsupported security scheme: %s", input.SecuritySchemeName)
		}
		got := input.RequestValidationInput.Request.Header.Get(input.SecurityScheme.Name)
		if subtle.ConstantTimeCompare([]byte(got), []byte(apiKey)) == 1 {
			return nil
		}
		// gin-middleware の ErrorHandler にはエラー文字列とステータスしか渡らないため、
		// 認証失敗であることを gin.Context 経由で伝えて 401 にする。
		if c := ginmiddleware.GetGinContext(ctx); c != nil {
			c.Set(authFailedKey, true)
		}
		return errors.New("invalid api key")
	}
}
