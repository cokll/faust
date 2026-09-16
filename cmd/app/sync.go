package faust

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/crazytaxii/faust/pkg/service"

	"github.com/docker/go-units"
	"github.com/qiniu/go-sdk/v7/storage"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
)

func syncCmd(gopts *GlobalOptions) *cli.Command {
	opts := gopts.NewSyncOptions()
	return &cli.Command{
		Name:  "sync",
		Usage: "Sync a local directory to the bucket, uploading only new or changed files",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := runSync(ctx, cmd, opts); err != nil {
				log.Error(err)
				return err
			}
			return nil
		},
		Flags: opts.Flags(),
	}
}

type SyncOptions struct {
	*GlobalOptions
	Dir         string
	Prefix      string
	Include     string
	Exclude     string
	ContentType string
	DryRun      bool
	Force       bool
	RefreshCDN  bool
}

func (o *GlobalOptions) NewSyncOptions() *SyncOptions {
	return &SyncOptions{
		GlobalOptions: o,
	}
}

func (o *SyncOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "dir",
			Aliases:     []string{"d"},
			Usage:       "`directory` to sync from",
			Destination: &o.Dir,
		},
		&cli.StringFlag{
			Name:        "prefix",
			Aliases:     []string{"p"},
			Usage:       "remote key `prefix` (default: the directory name)",
			Destination: &o.Prefix,
		},
		&cli.StringFlag{
			Name:        "include",
			Aliases:     []string{"i"},
			Usage:       "comma separated globs to include, e.g. \"*.min.js,css/*.css\"",
			Destination: &o.Include,
		},
		&cli.StringFlag{
			Name:        "exclude",
			Aliases:     []string{"x"},
			Usage:       "comma separated globs to exclude",
			Destination: &o.Exclude,
		},
		&cli.StringFlag{
			Name:        "content-type",
			Usage:       "force this MIME type instead of inferring from the extension",
			Destination: &o.ContentType,
		},
		&cli.BoolFlag{
			Name:        "dry-run",
			Aliases:     []string{"n"},
			Usage:       "show what would be uploaded without touching the bucket",
			Destination: &o.DryRun,
		},
		&cli.BoolFlag{
			Name:        "force",
			Aliases:     []string{"f"},
			Usage:       "upload every matched file, skipping the remote comparison",
			Destination: &o.Force,
		},
		&cli.BoolFlag{
			Name:        "refresh-cdn",
			Usage:       "refresh the CDN cache of the uploaded URLs (use --refresh-cdn=false to skip)",
			Value:       true,
			Destination: &o.RefreshCDN,
		},
	}
}

type syncItem struct {
	rel   string // 相对 --dir 的路径，统一用 /
	key   string // 远端 key
	local string // 本地绝对路径
	size  int64
}

// matchAny 判断相对路径是否命中 glob 列表。
// 带 / 的模式匹配整条相对路径（css/*.css），不带的只匹配文件名（*.min.js）。
func matchAny(patterns []string, rel string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		target := base
		if strings.Contains(p, "/") {
			target = rel
		}
		if ok, err := path.Match(p, target); err == nil && ok {
			return true
		}
	}
	return false
}

func splitGlobs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// short 安全截断摘要，避免日志里刷一长串。
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// objectMatchesLocal 判断远端对象是否已经等于本地文件。
// 返回 (是否确定一致, 原因说明)。
//
// 判定顺序（从可靠到不可靠）：
//  1. 大小不同 -> 一定要传。
//  2. stat 带回的 md5（hex）与本地一致 -> 跳过。这是最可靠的判据。
//  3. etag 恰是 base64/hex 形态的 md5 -> 跳过。
//  4. 其它情况（七牛现在的上传接口给的是 F 前缀摘要，stat 的 md5 又可能为空）
//     -> 判为「无法比对」重传一次。宁可多传，也不要静默漏传。
func objectMatchesLocal(fi storage.FileInfo, b64, hexSum string, size int64) (bool, string) {
	if fi.Fsize != size {
		return false, fmt.Sprintf("大小不同（远端 %d / 本地 %d）", fi.Fsize, size)
	}
	if fi.Md5 != "" {
		if strings.EqualFold(fi.Md5, hexSum) {
			return true, "md5 一致"
		}
		return false, fmt.Sprintf("md5 不同（远端 %s / 本地 %s）", short(fi.Md5), short(hexSum))
	}
	if fi.Hash == b64 || fi.Hash == hexSum {
		return true, "etag = md5"
	}
	return false, fmt.Sprintf("远端无 md5 元数据、etag 也非 md5（%s），无法比对", short(fi.Hash))
}

func runSync(ctx context.Context, cmd *cli.Command, opts *SyncOptions) error {
	if opts.Dir == "" {
		return cli.ShowSubcommandHelp(cmd)
	}
	cfg, err := opts.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("本地目录不可用：%w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("--dir 需要是目录：%s", dir)
	}

	prefix := strings.Trim(opts.Prefix, "/")
	if !cmd.IsSet("prefix") {
		prefix = filepath.Base(dir)
	}

	includes := splitGlobs(opts.Include)
	excludes := splitGlobs(opts.Exclude)

	var items []syncItem
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(includes) > 0 && !matchAny(includes, rel) {
			return nil
		}
		if matchAny(excludes, rel) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		key := rel
		if prefix != "" {
			key = prefix + "/" + rel
		}
		items = append(items, syncItem{rel: rel, key: key, local: p, size: fi.Size()})
		return nil
	})
	if err != nil {
		return err
	}

	si := service.NewQiniuService(cfg.QServiceConfig)
	log.WithFields(log.Fields{
		"bucket":  cfg.Bucket,
		"dir":     dir,
		"prefix":  prefix,
		"matched": len(items),
		"dry_run": opts.DryRun,
	}).Info("sync started")
	if len(items) == 0 {
		log.Warn("没有匹配到任何文件，检查一下 --include / --exclude")
		return nil
	}

	var (
		uploaded     []syncItem
		uploadedURLs []string
		skipped      int
		totalBytes   int64
	)

	for _, it := range items {
		b64, hexSum, size, err := service.MD5File(it.local)
		if err != nil {
			return fmt.Errorf("读取 %s 失败：%w", it.local, err)
		}

		if !opts.Force {
			remote, found, err := si.StatObject(it.key)
			if err != nil {
				return fmt.Errorf("查询远端 %s 失败：%w", it.key, err)
			}
			if found {
				if same, why := objectMatchesLocal(remote, b64, hexSum, size); same {
					skipped++
					fmt.Printf("  = %-40s 跳过（%s，%s）\n", it.key, why, units.HumanSize(float64(size)))
					continue
				} else {
					fmt.Printf("  > %-40s 待传（%s）\n", it.key, why)
				}
			} else {
				fmt.Printf("  + %-40s 待传（远端不存在）\n", it.key)
			}
		}

		if opts.DryRun {
			fmt.Printf("    [dry-run] 会以 %s 上传 %s -> %s\n",
				service.MimeByExt(it.local), it.local, it.key)
			continue
		}

		ictx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		res, err := si.UploadObject(ictx, it.local, it.key, opts.ContentType)
		cancel()
		if err != nil {
			return fmt.Errorf("上传 %s 失败：%w", it.key, err)
		}
		uploaded = append(uploaded, it)
		totalBytes += res.Size
		for _, u := range res.URLs {
			uploadedURLs = append(uploadedURLs, u)
		}
		fmt.Printf("  ✓ %-40s 已上传 %s  md5=%s\n",
			it.key, units.HumanSize(float64(res.Size)), hexSum[:8])
	}

	if opts.DryRun {
		fmt.Printf("\n[dry-run] 匹配 %d 个文件，未做任何改动\n", len(items))
		return nil
	}

	if len(uploaded) == 0 {
		log.WithField("skipped", skipped).Info("全部已是最新，无需上传")
		return nil
	}

	// 同名覆盖不会失效 CDN 缓存，刷一下才是「真的生效」
	if opts.RefreshCDN {
		code, reqID, err := si.RefreshCDN(uploadedURLs)
		if err != nil {
			log.WithFields(log.Fields{"code": code, "request_id": reqID, "err": err}).
				Warn("CDN 刷新失败 —— 对象已上传，但边缘可能仍在吃旧缓存；可在七牛控制台手动刷新")
		} else {
			log.WithFields(log.Fields{"code": code, "request_id": reqID, "urls": len(uploadedURLs)}).
				Info("CDN 刷新任务已提交")
		}
	}

	log.WithFields(log.Fields{
		"uploaded": len(uploaded),
		"skipped":  skipped,
		"bytes":    units.HumanSize(float64(totalBytes)),
		"bucket":   cfg.Bucket,
	}).Info("sync done")
	for _, it := range uploaded {
		if urls, err := si.ObjectURLs(it.key); err == nil {
			for _, u := range urls {
				fmt.Printf("  -> %s\n", u)
			}
		}
	}
	return nil
}
