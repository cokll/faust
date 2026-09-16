package service

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/qiniu/go-sdk/v7/cdn"
	"github.com/qiniu/go-sdk/v7/storage"
)

// 通用对象上传（非图片）：按显式 key 写入任意文件。
//
// 为什么要单独开一条路：UploadImage 会先跑 image.DecodeConfig 探测图片格式，
// js / css / wasm 这类前端产物过不了那道门
//
// 关于 etag —— 这是「只传变更文件」能不能做准的关键，踩过坑：
// storagev2 的 UploadManager 虽然对 <= 4MB 的文件走「表单上传」，但它内部用的是
// 七牛 v3 上传接口，返回的 etag 一律是 **F 前缀的摘要**（连单次上传也是），
// 跟文件 md5 对不上，没法拿来做比对。所以这里刻意退回 **v1 的经典表单上传
// （storage.FormUploader.PutFile → POST /upload）**：那条路单次上传的 etag
// 就是 base64(md5)，可以精确比对。
//   - 我们自己传上去的文件，下次能靠 etag 判断「没变，跳过」；
//   - 别人（控制台 / 其它工具）传上去的老对象 etag 是 F 前缀，第一次会被判为
//     「无法比对」而重传一次，之后 etag 归位，比对就准了。
//
// MIME 必须显式给对：七牛把 ContentType 存进对象元数据、CDN 直接回这个头。
// .js 若被存成 application/json，浏览器在有 nosniff 的场合会拒绝执行。

type ObjectUploadResponse struct {
	Bucket string
	Key    string
	Size   int64
	Hash   string
	URLs   []string
}

var mimeByExt = map[string]string{
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".cjs":   "text/javascript",
	".css":   "text/css",
	".json":  "application/json",
	".map":   "application/json",
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "text/xml; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".wasm":  "application/wasm",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",
	".zip":   "application/zip",
	".7z":    "application/x-7z-compressed",
	".pdf":   "application/pdf",
	".mp3":   "audio/mpeg",
	".mp4":   "video/mp4",
}

// MimeByExt 按扩展名推断 MIME，认不出来时回退 octet-stream。
func MimeByExt(name string) string {
	if m, ok := mimeByExt[strings.ToLower(path.Ext(name))]; ok {
		return m
	}
	return "application/octet-stream"
}

// MD5File 返回文件的 base64（标准字母表，带 padding）与 hex 两种 md5。
// 七牛的 etag 用 base64 形态，人看日志时 hex 更直观，两个都给。
func MD5File(path string) (b64 string, hexSum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = f.Close() }()

	h := md5.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", "", 0, err
	}
	sum := h.Sum(nil)
	return base64.StdEncoding.EncodeToString(sum), hex.EncodeToString(sum), n, nil
}

// wrapQiniuErr 给 SDK 的原始报错补一句人话。
//
// 最常见的坑：本机开了 HTTP 代理（HTTPS_PROXY），代理把七牛 API 的响应改写了，
// SDK 解析不出来就丢一句 "malicious response" —— 看着像服务端故障，其实只是
// 代理在中间捣乱。七牛是国内直连服务，绕过代理即可。
func wrapQiniuErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "malicious response") {
		return fmt.Errorf("%w —— 疑似被 HTTP 代理改写了响应。"+
			"七牛是国内直连服务，请清掉 HTTP_PROXY/HTTPS_PROXY，或把 "+
			"*.qiniu.com / *.qiniuapi.com 加进 NO_PROXY", err)
	}
	if strings.Contains(err.Error(), "file exists") {
		return fmt.Errorf("%w —— 上传凭证是 insert-only，不允许覆盖同名对象。"+
			"PutPolicy.Scope 要写成 `bucket:key`（裸 `bucket` 只能新建）", err)
	}
	return err
}

// StatObject 查远端对象元数据。第二个返回值表示「对象存在」。
func (s *QiniuService) StatObject(key string) (storage.FileInfo, bool, error) {
	fi, err := s.bucketManager.Stat(s.config.Bucket, key)
	if err != nil {
		// 612 no such file or directory（大小写/措辞随服务端版本略有出入）
		if strings.Contains(strings.ToLower(err.Error()), "no such file") {
			return storage.FileInfo{}, false, nil
		}
		return storage.FileInfo{}, false, wrapQiniuErr(err)
	}
	return fi, true, nil
}

// Domains 列出 bucket 绑定的所有 CDN 域名（用于拼公网 URL / 刷缓存）。
func (s *QiniuService) Domains() ([]string, error) {
	info, err := s.bucketManager.ListBucketDomains(s.config.Bucket)
	if err != nil {
		return nil, wrapQiniuErr(err)
	}
	out := make([]string, 0, len(info))
	for _, d := range info {
		if d.Domain != "" {
			out = append(out, d.Domain)
		}
	}
	return out, nil
}

// ObjectURLs 拼出对象在各域名下的公网地址。
func (s *QiniuService) ObjectURLs(key string) ([]string, error) {
	domains, err := s.Domains()
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(domains))
	for _, d := range domains {
		urls = append(urls, fmt.Sprintf("https://%s/%s", d, key))
	}
	return urls, nil
}

// UploadObject 把本地文件按指定 key 上传。contentType 为空则按扩展名推断。
func (s *QiniuService) UploadObject(ctx context.Context, localPath, key, contentType string) (*ObjectUploadResponse, error) {
	if contentType == "" {
		contentType = MimeByExt(localPath)
	}
	fi, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}

	// Scope 必须写成 `bucket:key`。裸 `bucket` 是 **insert-only**：
	// 内容/etag 一致时七牛直接返回成功（其实什么都没做），内容不一致就报
	// `file exists` —— 覆盖同名对象会整个失败。带上 key 才允许覆盖。
	policy := storage.PutPolicy{
		Scope:      s.config.Bucket + ":" + key,
		Expires:    s.config.Expires,
		ReturnBody: fmtKodoReturnBody,
	}
	upToken := policy.UploadToken(s.credentials)

	var ret storage.PutRet
	err = s.formUploader.PutFile(ctx, &ret, upToken, key, localPath, &storage.PutExtra{
		MimeType: contentType,
	})
	if err != nil {
		return nil, wrapQiniuErr(err)
	}

	res := &ObjectUploadResponse{
		Bucket: s.config.Bucket,
		Key:    key,
		Size:   fi.Size(),
		Hash:   ret.Hash,
	}
	res.URLs, _ = s.ObjectURLs(key)
	return res, nil
}

// DeleteObject 删除对象（回滚用）。
func (s *QiniuService) DeleteObject(key string) error {
	return s.bucketManager.Delete(s.config.Bucket, key)
}

// RefreshCDN 提交 URL 刷新任务。
// 同名覆盖不会自动失效 CDN 缓存（按路径长缓存），部署后不刷就可能继续吃旧内容，
// 所以这一步是「传完了到底生不生效」的必要收尾。失败不算上传失败——只是缓存
// 可能还是旧的，调用方据此给出提示。
func (s *QiniuService) RefreshCDN(urls []string) (int, string, error) {
	if len(urls) == 0 {
		return 0, "", nil
	}
	// 单次上限 100 条，超出分批
	mgr := cdn.NewCdnManager(s.credentials)
	code, reqID := 0, ""
	for i := 0; i < len(urls); i += 100 {
		end := i + 100
		if end > len(urls) {
			end = len(urls)
		}
		ret, err := mgr.RefreshUrls(urls[i:end])
		if err != nil {
			return ret.Code, ret.RequestID, err
		}
		code, reqID = ret.Code, ret.RequestID
		// 注意：七牛刷新接口**成功**时也回 {"code":200,"error":"success"}，
		// 只有 code != 200 才是真失败。别拿 Error 非空当失败判据。
		if ret.Code != 200 {
			msg := ret.Error
			if msg == "" {
				msg = fmt.Sprintf("unexpected code %d", ret.Code)
			}
			return ret.Code, ret.RequestID, fmt.Errorf("%s", msg)
		}
	}
	return code, reqID, nil
}
