# faust ![CI Status](https://github.com/crazytaxii/faust/actions/workflows/ci.yaml/badge.svg)

faust 是一款将本地图片上传至七牛云对象存储的小工具。目前支持：

- jpg
- png
- webp
- gif
- [avif](https://aomediacodec.github.io/av1-avif/)
- svg

## 编译

**请先配置好 Go 开发环境！**

```bash
$ make build
```

## 配置

请先注册七牛云账号，并获取账号对应的 Access Key 和 Secret Key：

1. 点击[注册](https://portal.qiniu.com/signup?ref=developer.qiniu.com)开通七牛开发者帐号
2. 如果已有账号，直接登录七牛开发者后台，点击[这里](https://portal.qiniu.com/user/key)查看 Access Key 和 Secret Key

- Access Key & Secret Key
- Bucket
- Base URL (已绑定存储空间的融合 CDN 加速域名，比如 <https://pic.crazytaxii.com>)

**[域名接入七牛云存储](https://developer.qiniu.com/fusion/manual/4939/the-domain-name-to-access)**

```bash
$ cat <<EOF > ~/.faust/config.yaml
accessKey ${your_access_key} \
secretKey ${your_secret_key} \
bucket ${bucket_name} \
baseURL ${your_base_url}
EOF
```

> 配置文件 config.yaml 默认放置于 ~/.faust 路径

## 使用

1. 上传图片

    ```bash
    $ faust upload --image ./test/Go-Logo_Fuchsia.jpg
    ```

1. 上传证书（私钥 + 证书链）

    ```bash
    $ faust upload --key /path/to/private.key --cert /path/to/fullchain.cer
    ```

1. 上传任意文件（js / css / wasm 等非图片产物）

    ```bash
    $ faust upload --file ./js/test/test.min.js \
                   --remote test/test.min.js
    ```

    远端 key 用 `--remote`（`--key` 已被证书私钥占用）。MIME 按扩展名推断，
    可用 `--content-type` 覆盖。传完默认提交一次 CDN 刷新。

1. 同步目录（只传新增/变更的文件）

    ```bash
    $ faust sync --dir js/test --prefix test \
                 --include "*.min.js,css/*.css"
    ```

    - 拿远端对象的 md5 和本地比，一致就跳过；`--force` 强制全传，
      `--dry-run` 只列不改。`--prefix` 不给时取目录名。
    - `--include` / `--exclude` 为逗号分隔 glob：**带 `/` 的匹配相对路径**
      （`css/*.css`），**不带的只匹配文件名**（`*.min.js`）。
    - 传完默认提交 CDN 刷新，`--refresh-cdn=false` 可关。七牛 CDN 按路径长缓存、
      **同名覆盖不会失效缓存**，不刷就等于没部署。
    - 域名从 bucket 绑定关系里取（`ListBucketDomains`），用来拼公网 URL 和刷缓存。

    > 三个坑：
    > 1. **上传凭证的 `Scope` 必须写成 `bucket:key`**。裸 `bucket` 是 insert-only：
    >    内容/etag 一致时七牛直接返回成功（其实什么都没做，很容易误以为「传上去了」），
    >    内容不一致则报 `file exists`，覆盖同名对象整个失败。
    > 2. 本机若挂着 `HTTP_PROXY` / `HTTPS_PROXY`，代理会改写七牛 API 的响应，
    >    SDK 只报一句 `malicious response`。七牛是国内直连服务，
    >    清掉代理变量、或把 `*.qiniu.com` / `*.qiniuapi.com` 加进 `NO_PROXY`。
    > 3. 七牛现在的上传接口（v3，以及 v1 `FormUploader` 的内部实现）返回的 etag
    >    一律是 `F` 前缀的摘要，**不是 md5**；所以「是否变更」靠 `stat` 响应里的
    >    `md5` 字段判断，不要拿 etag 当 md5 用。老对象若没有 md5 元数据，
    >    会被判为「无法比对」而重传一次——宁可多传，不要静默漏传。

1. 删除图片

    ```bash
    $ faust delete --key 26-07-01/49688378.jpeg
    ```
