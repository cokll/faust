package faust

import "github.com/urfave/cli/v3"

type GlobalOptions struct {
	// path of config file
	ConfigFile string
}

func NewGlobalOptions() *GlobalOptions {
	return &GlobalOptions{}
}

func (o *GlobalOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "config-file",
			Aliases:     []string{"c"},
			Usage:       "`file` path of configuration to be loaded",
			Destination: &o.ConfigFile,
		},
	}
}

func (o *GlobalOptions) LoadConfig() (*Config, error) {
	// load config file first
	return TryToLoadConfig(o.ConfigFile)
}

func (o *GlobalOptions) NewUploadOptions() *UploadOptions {
	return &UploadOptions{
		GlobalOptions: o,
	}
}

func (o *GlobalOptions) NewDeleteOptions() *DeleteOptions {
	return &DeleteOptions{
		GlobalOptions: o,
	}
}

type UploadOptions struct {
	*GlobalOptions
	ImagePath   string
	CertPath    string
	KeyPath     string
	FilePath    string
	RemoteKey   string
	ContentType string
	RefreshCDN  bool
}

func (o *UploadOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "image",
			Aliases:     []string{"i"},
			Usage:       "`file` path of image to be uploaded",
			Destination: &o.ImagePath,
		},
		&cli.StringFlag{
			Name:        "cert",
			Usage:       "`file` path of certificate to be uploaded",
			Destination: &o.CertPath,
		},
		&cli.StringFlag{
			Name:        "key",
			Usage:       "`file` path of private key to be uploaded",
			Destination: &o.KeyPath,
		},
		// 下面是通用文件上传（js/css/wasm…）。注意 --key 已被证书私钥占用，
		// 远端 key 用 --remote，别复用。
		&cli.StringFlag{
			Name:        "file",
			Aliases:     []string{"f"},
			Usage:       "`file` path of an arbitrary file to be uploaded (non-image)",
			Destination: &o.FilePath,
		},
		&cli.StringFlag{
			Name:        "remote",
			Aliases:     []string{"r"},
			Usage:       "remote object `key` for --file, e.g. saveupload/saveupload.min.js",
			Destination: &o.RemoteKey,
		},
		&cli.StringFlag{
			Name:        "content-type",
			Usage:       "force this MIME type instead of inferring from the extension",
			Destination: &o.ContentType,
		},
		&cli.BoolFlag{
			Name:        "refresh-cdn",
			Usage:       "refresh the CDN cache of the uploaded object (only for --file)",
			Value:       true,
			Destination: &o.RefreshCDN,
		},
	}
}

type DeleteOptions struct {
	*GlobalOptions
	Key string
}

func (o *DeleteOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "key",
			Usage:       "key of image to be deleted",
			Destination: &o.Key,
		},
	}
}
