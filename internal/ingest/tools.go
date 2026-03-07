package ingest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd := exec.CommandContext(ctx, p.path, "copy", src, fmt.Sprintf("%s[strip]", dst), "--flatten", "--interpretation", "srgb")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vips copy failed: %s", commandError(out, err))
	}
	return nil
}

func (p *vipsProcessor) Resize(ctx context.Context, src, dst string, maxSize int) error {
	size := fmt.Sprintf("%d", maxSize)
	tmpFile, err := os.CreateTemp(filepath.Dir(dst), ".thumb-*.jpg")
	if err != nil {
		return fmt.Errorf("create temp thumbnail file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp thumbnail file: %w", err)
	}
	defer os.Remove(tmpPath)

	cmd := exec.CommandContext(ctx, p.path, "thumbnail", src, tmpPath, size)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vips thumbnail failed: %s", commandError(out, err))
	}

	cmd = exec.CommandContext(ctx, p.path, "copy", tmpPath, fmt.Sprintf("%s[strip]", dst))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vips strip failed: %s", commandError(out, err))
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
