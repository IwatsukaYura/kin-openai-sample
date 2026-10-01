# kin-openapi validation sample

OpenAPI 定義から [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) で生成した Go の API サーバーに対して、
[kin-openapi](https://github.com/getkin/kin-openapi) でリクエスト / レスポンスをどうバリデーションできるかを確認するためのサンプルです。

## 構成

```
api/
  openapi.yaml          # OpenAPI 定義 (ここが唯一の正)
  oapi-codegen.yaml     # oapi-codegen の設定 (std-http-server + models + embedded-spec)
  generate.go           # go:generate
  api.gen.go            # 生成コード (手で編集しない)
internal/
  validator/            # kin-openapi を直接使ったバリデーションミドルウェア
  handler/              # api.ServerInterface の実装 (インメモリ)
cmd/server/             # エントリーポイント + 結合テスト
```

## 使い方

```sh
go generate ./...         # openapi.yaml から api/api.gen.go を再生成
go test ./...             # バリデーションの挙動をテストで確認
go run ./cmd/server       # :8080 で起動 (-validate-response=false でレスポンス検証オフ)
```

```sh
# 正常系
curl -X POST localhost:8080/users -H 'X-API-Key: secret' -H 'Content-Type: application/json' \
  -d '{"name":"taro","email":"taro@example.com","role":"admin","tags":["go"]}'

# 異常系 (MultiError: true なので全部のエラーがまとめて返る)
curl -X POST localhost:8080/users -H 'X-API-Key: secret' -H 'Content-Type: application/json' \
  -d '{"name":"","email":"bad","role":"guest","age":-1,"extra":1}'
# => 400
# {"message":"request validation failed","errors":[
#   {"field":"body.age","reason":"number must be at least 0"},
#   {"field":"body.email","reason":"string doesn't match the format \"email\" ..."},
#   {"field":"body","reason":"property \"extra\" is unsupported"},
#   {"field":"body.name","reason":"minimum string length is 1"},
#   {"field":"body.role","reason":"value is not one of the allowed values [\"admin\",\"member\"]"}]}

curl 'localhost:8080/users?limit=0&role=guest'
# => 400 query.limit / query.role のエラー
```

## バリデーションの流れ

`internal/validator/validator.go` で、kin-openapi の API を順に呼んでいます。

1. `api.GetSpec()` — oapi-codegen が埋め込んだ定義を `*openapi3.T` として取得
2. `gorillamux.NewRouter(spec)` → `router.FindRoute(r)` — リクエストに対応するオペレーションを特定 (未定義なら 404 / 405)
3. `openapi3filter.ValidateRequest` — path / query / header パラメーター、リクエストボディ、security を検証
4. (任意) ハンドラーのレスポンスをバッファしておき `openapi3filter.ValidateResponse` で検証

返ってくるエラーは以下のような型の入れ子になっているので、`toErrorResponse` で剥がして `field` / `reason` の配列に変換しています。

| 型 | 意味 |
| --- | --- |
| `openapi3.MultiError` | `Options.MultiError: true` のときの複数エラー |
| `*openapi3filter.RequestError` | どのパラメーター / ボディでのエラーか (`Parameter`, `RequestBody`) |
| `*openapi3filter.SecurityRequirementsError` | 認証エラー (401 にしている) |
| `*openapi3filter.ResponseError` | レスポンスのエラー |
| `*openapi3.SchemaError` | スキーマ違反の詳細。`JSONPointer()` で `tags/0` のような位置が取れる |

## 検証してわかったこと・ハマりどころ

- **`format: email` はデフォルトでは検証されない。** kin-openapi が組み込みで検証するのは `date` / `date-time` / `byte` / `int32` / `int64` だけ。
  `openapi3.DefineStringFormatValidator("email", ...)` で自分で登録する必要がある (`validator.go` の `init`)。
- **security を定義したら `AuthenticationFunc` が必須。** 未設定だとそのオペレーションへのリクエストは全部失敗する。
  API キーの照合などはこの関数の中で自分で書く。
- **`MultiError: true` にしないと最初の 1 件で止まる。** フォームのように全エラーを返したい場合は有効にする。
- **`errors.As` で `MultiError` を判定すると順序がおかしくなる。** `openapi3.MultiError` は `As` メソッドを持っていて、
  `RequestError` の中の `MultiError` に先にマッチしてしまい、どのパラメーターのエラーか (`Parameter`) が取れなくなる。
  外側から type switch で剥がすのが確実。
- **`servers` を書くと Host ヘッダーまで照合される。** ローカルや別ホストから叩くと `no matching operation was found` になるので、
  ルーター作成前に `spec.Servers = nil` にしている。
- **ミドルウェアをどこに挟むか。** oapi-codegen の `StdHTTPServerOptions.Middlewares` は生成コードがパスパラメーターをバインドした
  *後* に実行されるため、`/users/abc` のような型違いは kin-openapi に届く前に生成コード側のエラーになる。
  このサンプルでは `v.Handler(api.Handler(...))` と mux 全体をラップして、kin-openapi を最初に通している。
- **`additionalProperties: false`** の違反は `property "extra" is unsupported` になり、`JSONPointer` はオブジェクト自体 (`body`) を指す。
- **レスポンス検証** では `IncludeResponseStatus: true` にすると、定義にないステータスコードを返したときに `status is not supported` で検出できる。
  ボディ全体をバッファする必要があるので、本番では無効化 (開発・テスト時のみ有効) にする使い方が現実的。

## 補足: nethttp-middleware を使う場合

同じことを oapi-codegen 公式の [`github.com/oapi-codegen/nethttp-middleware`](https://github.com/oapi-codegen/nethttp-middleware) でも実現できます
(内部で同じく `openapi3filter.ValidateRequest` を呼んでいる)。

```go
mw := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
    Options: openapi3filter.Options{MultiError: true, AuthenticationFunc: authenticate},
})
http.ListenAndServe(":8080", mw(api.Handler(handler.New())))
```

レスポンス検証やエラーレスポンスの形を細かく制御したい場合は、このサンプルのように kin-openapi を直接使う方が見通しが良いです。
