package ingest

import (
	"context"
	"fmt"
	"os/exec"
)

type ImageProcessor interface {
	Normalize(ctx context.Context, src, dst string) error
	Resize(ctx context.Context, src, dst string, maxSize int) error
	Name() string
}

func NewImageProcessor() (ImageProcessor, error) {
	if path, err := exec.LookPath("vips"); err == nil {
		return &vipsProcessor{path: path}, nil
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return &ffmpegProcessor{path: path}, nil
	}
	return nil, fmt.Errorf("image processing requires vips or ffmpeg in PATH")
}

type vipsProcessor struct {
	path string
}

func (p *vipsProcessor) Name() string {
	return "vips"
}

func (p *vipsProcessor) Normalize(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, p.path, "copy", src, dst, "--strip", "--flatten", "--interpretation", "srgb")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vips copy failed: %s", commandError(out, err))
	}
	return nil
}

func (p *vipsProcessor) Resize(ctx context.Context, src, dst string, maxSize int) error {
	size := fmt.Sprintf("%d", maxSize)
	cmd := exec.CommandContext(ctx, p.path, "thumbnail", src, dst, size, "--strip")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vips thumbnail failed: %s", commandError(out, err))
	}
	return nil
}

type ffmpegProcessor struct {
	path string
}

func (p *ffmpegProcessor) Name() string {
	return "ffmpeg"
}

func (p *ffmpegProcessor) Normalize(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, p.path, "-y", "-i", src, "-map_metadata", "-1", "-vf", "format=rgb24", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg normalize failed: %s", commandError(out, err))
	}
	return nil
}

func (p *ffmpegProcessor) Resize(ctx context.Context, src, dst string, maxSize int) error {
	scale := fmt.Sprintf("scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", maxSize, maxSize)
	cmd := exec.CommandContext(ctx, p.path, "-y", "-i", src, "-map_metadata", "-1", "-vf", scale, "-q:v", "3", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg resize failed: %s", commandError(out, err))
	}
	return nil
}

func commandError(out []byte, err error) string {
	msg := string(out)
	if msg == "" {
		return err.Error()
	}
	return msg
}
