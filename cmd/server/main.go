package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/IwatsukaYura/kin-openai-sample/api"
	"github.com/IwatsukaYura/kin-openai-sample/internal/handler"
	"github.com/IwatsukaYura/kin-openai-sample/internal/validator"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	validateResponse := flag.Bool("validate-response", true, "validate responses against the openapi spec")
	flag.Parse()

	h, err := NewHandler(*validateResponse)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}

// NewHandler は oapi-codegen の生成ハンドラーに kin-openapi のバリデーションを被せたものを返す。
func NewHandler(validateResponse bool) (http.Handler, error) {
	// oapi-codegen が埋め込んだ OpenAPI 定義を kin-openapi の *openapi3.T として取得
	spec, err := api.GetSpec()
	if err != nil {
		return nil, err
	}
	v, err := validator.New(spec, validator.Config{ValidateResponse: validateResponse})
	if err != nil {
		return nil, err
	}
	// 生成コードの StdHTTPServerOptions.Middlewares はパスパラメーターの
	// バインド後に実行されるため、ここでは mux 全体をラップして
	// 生成コードより先に kin-openapi のバリデーションを通す。
	return v.Handler(api.Handler(handler.New())), nil
}
