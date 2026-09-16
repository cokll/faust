package faust

import (
	"context"
	"errors"
	"fmt"

	"github.com/crazytaxii/faust/pkg/service"

	"github.com/docker/go-units"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
)

func init() {
	// 干脆不上色。上游写的是 ForceColors: true，于是无论输出是不是终端都塞 ANSI
	// 转义 —— 重定向/管道捕获时会看到 "Xbucket=..." 这种乱码，老版 conhost
	// （未开 VT）也会原样打印转义序列。faust 一次就输出几行日志，颜色不值这个风险。
	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
}

func NewFaustApp(ver string) *cli.Command {
	gopts := NewGlobalOptions()
	return &cli.Command{
		Name:    "faust",
		Usage:   "A simple tool for uploading image to object storage service",
		Version: ver,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return cli.ShowAppHelp(cmd)
		},
		Commands: []*cli.Command{
			uploadCmd(gopts),
			deleteCmd(gopts),
			syncCmd(gopts),
		},
		Flags: gopts.Flags(),
	}
}

func uploadCmd(gopts *GlobalOptions) *cli.Command {
	opts := gopts.NewUploadOptions()
	return &cli.Command{
		Name:    "upload",
		Aliases: []string{"up"},
		Usage:   "Upload image or certificates to object storage service",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := runUpload(ctx, cmd, opts); err != nil {
				log.Error(err)
				return err
			}
			return nil
		},
		Flags: opts.Flags(),
	}
}

func runUpload(ctx context.Context, cmd *cli.Command, opts *UploadOptions) error {
	cfg, err := opts.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	si := service.NewQiniuService(cfg.QServiceConfig)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	lf := make(log.Fields)
	if opts.ImagePath != "" {
		res, err := si.UploadImage(ctx, opts.ImagePath)
		if err != nil {
			return fmt.Errorf("error uploading image: %w", err)
		}
		lf["bucket"] = res.Bucket
		lf["key"] = res.Key
		lf["size"] = units.HumanSize(float64(res.Size))
		lf["image_url"] = res.URLs
	} else if opts.CertPath != "" && opts.KeyPath != "" {
		res, err := si.UploadCerts(ctx, opts.KeyPath, opts.CertPath)
		if err != nil {
			return fmt.Errorf("error uploading certificates: %w", err)
		}
		lf["common_name"] = res.CommonName
		lf["expiration"] = res.Expiration
	} else if opts.FilePath != "" {
		if opts.RemoteKey == "" {
			return errors.New("--file requires --remote <object key>")
		}
		res, err := si.UploadObject(ctx, opts.FilePath, opts.RemoteKey, opts.ContentType)
		if err != nil {
			return fmt.Errorf("error uploading file: %w", err)
		}
		lf["bucket"] = res.Bucket
		lf["key"] = res.Key
		lf["size"] = units.HumanSize(float64(res.Size))
		lf["object_url"] = res.URLs
		// 同名覆盖不会失效 CDN 缓存，不刷就可能继续吃旧内容
		if opts.RefreshCDN {
			if code, reqID, err := si.RefreshCDN(res.URLs); err != nil {
				log.WithFields(log.Fields{"code": code, "request_id": reqID, "err": err}).
					Warn("CDN refresh failed; the object is uploaded but the edge may still serve the old copy")
			}
		}
	} else {
		return cli.ShowSubcommandHelp(cmd)
	}

	log.WithFields(lf).Info("upload successfully")
	return nil
}

func deleteCmd(gopts *GlobalOptions) *cli.Command {
	opts := gopts.NewDeleteOptions()
	return &cli.Command{
		Name:    "delete",
		Aliases: []string{"del"},
		Usage:   "Delete image from object storage service",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := runDelete(ctx, cmd, opts); err != nil {
				log.Error(err)
				return err
			}
			return nil
		},
		Flags: opts.Flags(),
	}
}

func runDelete(ctx context.Context, cmd *cli.Command, opts *DeleteOptions) error {
	cfg, err := opts.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	si := service.NewQiniuService(cfg.QServiceConfig)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	if opts.Key != "" {
		if err := si.DeleteImage(ctx, opts.Key); err != nil {
			return fmt.Errorf("error deleting image: %w", err)
		}
	} else {
		return cli.ShowSubcommandHelp(cmd)
	}

	log.WithFields(log.Fields{
		"key":    opts.Key,
		"bucket": cfg.QServiceConfig.Bucket,
	}).Info("delete successfully")
	return nil
}
