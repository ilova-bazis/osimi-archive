package ingest

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type AudioProcessor interface {
	EncodeAccess(ctx context.Context, src, dst string) error
	Name() string
}

type VideoProcessor interface {
	EncodeAccess(ctx context.Context, src, dst string) error
	Name() string
}

type DocumentProcessor interface {
	ToPDF(ctx context.Context, src, dst string) error
	Name() string
}

func NewAudioProcessor() (AudioProcessor, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH")
	}
	return &ffmpegAudioProcessor{path: path}, nil
}

func NewVideoProcessor() (VideoProcessor, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH")
	}
	return &ffmpegVideoProcessor{path: path}, nil
}

func NewDocumentProcessor() (DocumentProcessor, error) {
	path, err := exec.LookPath("soffice")
	if err != nil {
		return nil, fmt.Errorf("soffice not found in PATH")
	}
	return &sofficeProcessor{path: path}, nil
}

type ffmpegAudioProcessor struct {
	path string
}

func (p *ffmpegAudioProcessor) Name() string {
	return "ffmpeg"
}

func (p *ffmpegAudioProcessor) EncodeAccess(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, p.path, "-y", "-i", src, "-vn", "-c:a", "aac", "-b:a", "192k", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg audio failed: %s", commandError(out, err))
	}
	return nil
}

type ffmpegVideoProcessor struct {
	path string
}

func (p *ffmpegVideoProcessor) Name() string {
	return "ffmpeg"
}

func (p *ffmpegVideoProcessor) EncodeAccess(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, p.path, "-y", "-i", src, "-c:v", "libx264", "-crf", "23", "-preset", "medium", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg video failed: %s", commandError(out, err))
	}
	return nil
}

type sofficeProcessor struct {
	path string
}

func (p *sofficeProcessor) Name() string {
	return "soffice"
}

func (p *sofficeProcessor) ToPDF(ctx context.Context, src, dst string) error {
	outDir := filepath.Dir(dst)
	cmd := exec.CommandContext(ctx, p.path, "--headless", "--convert-to", "pdf", "--outdir", outDir, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("soffice convert failed: %s", commandError(out, err))
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	converted := filepath.Join(outDir, base+".pdf")
	if converted == dst {
		return nil
	}
	if _, err := copyFileAtomic(converted, dst); err != nil {
		return err
	}
	return nil
}
