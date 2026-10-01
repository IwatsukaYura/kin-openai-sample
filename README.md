# kin-openapi validation sample (Gin + oapi-codegen strict-server)

OpenAPI 定義から [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) で Gin の strict-server を生成し、
リクエストのバリデーションを oapi-codegen 公式の [gin-middleware](https://github.com/oapi-codegen/gin-middleware)
(内部で [kin-openapi](https://github.com/getkin/kin-openapi) を使用) に任せる構成のサンプルです。

## 構成

```
api/
  openapi.yaml          # OpenAPI 定義 (唯一の正)
  oapi-codegen.yaml     # gin-server + strict-server + models + embedded-spec
  generate.go           # go:generate
  api.gen.go            # 生成コード (手で編集しない)
internal/
  server/               # Gin エンジンの組み立て (バリデーター・エラーハンドリング・認証)
  handler/              # api.StrictServerInterface の実装 (業務ルールのみ)
  store/                # 永続化 (サンプルなのでインメモリ)
cmd/server/             # エントリーポイント (環境変数・タイムアウト・graceful shutdown)
```

## 使い方

```sh
go generate ./...                          # openapi.yaml から api/api.gen.go を再生成
go test ./...                              # バリデーションの挙動をテストで確認
API_KEY=secret GIN_MODE=release go run ./cmd/server   # PORT (デフォルト 8080)
```

```sh
curl -X POST localhost:8080/users -H 'X-API-Key: secret' -H 'Content-Type: application/json' \
  -d '{"name":"taro","email":"taro@example.com","role":"admin","tags":["go"]}'
# => 201

curl -X POST localhost:8080/users -H 'X-API-Key: secret' -H 'Content-Type: application/json' \
  -d '{"name":"","email":"bad","role":"guest","age":-1}'
# => 400 {"message":"request body has an error: doesn't match schema #/components/schemas/NewUser:
#          Error at \"/age\": number must be at least 0 | Error at \"/email\": ... | ..."}

curl 'localhost:8080/users?limit=0&role=guest'
# => 400 {"message":"parameter \"limit\" in query has an error: number must be at least 1 | parameter \"role\" ..."}
```

## 責務の分担

| 層 | 担当 | 例 |
| --- | --- | --- |
| `gin-middleware` (kin-openapi) | OpenAPI 定義で表現できる入力チェック | 型、必須、min/max、enum、pattern、format、additionalProperties、security |
| strict-server (生成コード) | 型付きのリクエスト / レスポンスへの変換 | `CreateUserRequestObject`、`CreateUser201JSONResponse` |
| `handler` | 定義では表現できない業務ルール | メールアドレス重複 → 409 |
| テスト | レスポンスが定義どおりか | `openapi3filter.ValidateResponse` で全レスポンスを検証 |

## 本番運用を意識したポイント

- **バリデーターはルーターグループに付ける。** 生成コードの `GinServerOptions.Middlewares` はパスパラメーターのバインド *後* に実行されるため、
  `/users/abc` のような型違いが kin-openapi に届かない。`r.Group("", validator)` に `RegisterHandlers` することで先にチェックする。
  `/healthz` のような定義外のエンドポイントはグループの外に置く。
- **`MultiError: true`** で全てのエラーをまとめて返す。`MultiErrorHandler` を指定しないと `multiple errors encountered: ` が前に付く。
- **`openapi3.SchemaErrorDetailsDisabled = true`** にして、エラーメッセージにスキーマ定義や入力値全体がダンプされないようにする。
- **`format: email` はデフォルトでは検証されない。** `openapi3.DefineStringFormatValidator` で kin-openapi 同梱の `FormatOfStringForEmail` を登録する。
- **security を定義したら `AuthenticationFunc` が必須。** 未設定だとそのオペレーションは常に失敗する。
  gin-middleware の `ErrorHandler` にはメッセージ文字列とステータス (400/404) しか渡らないため、
  `ginmiddleware.GetGinContext(ctx)` で認証失敗を gin.Context に記録し、`ErrorHandler` で 401 に変えている。
- **`spec.Servers = nil`**。servers が書かれていると Host ヘッダーまで照合され `no matching operation was found` になる。
- 起動時に `spec.Validate` で定義そのものの誤りを検出する。
- strict-server の `HandlerErrorFunc` / `ResponseErrorHandlerFunc` では内部エラーをログに出し、クライアントには詳細を返さない。
- `http.Server` のタイムアウト設定と SIGTERM での graceful shutdown。
- **レスポンス検証はテストでだけ行う。** 本番でやるとボディのバッファリングと検証のコストがかかるため。
  `IncludeResponseStatus: true` で、定義していないステータスコードを返した場合も検出する。

## gin-middleware の制約

- エラーは文字列でしか `ErrorHandler` に渡らないため、`{"field": ..., "reason": ...}` のような構造化したエラーは返せない。
  構造化が必要なら、`openapi3filter.ValidateRequest` を直接呼ぶミドルウェアを自前で書くことになる。
- 定義にないメソッドは 405 ではなく 400 (`method not allowed`) になる。
  このサンプルでは Gin の `HandleMethodNotAllowed` を有効にしているので、Gin のルーティングで先に 405 が返る。
