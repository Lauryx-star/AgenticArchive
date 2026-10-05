package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Extractor interface {
	Extract(context.Context, string) ([]Page, error)
}
type PDFExtractor struct {
	Languages    string
	MaxDimension int
	Timeout      time.Duration
	TempRoot     string
}

func (e PDFExtractor) command(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	// Tool output is discarded: document text goes to bounded per-page files.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", name, ctx.Err())
		}
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func readText(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 4<<20 {
		return "", fmt.Errorf("page text exceeds 4 MiB limit")
	}
	b, err := os.ReadFile(path)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\f", "")), err
}

func (e PDFExtractor) Extract(ctx context.Context, path string) ([]Page, error) {
	pages := []Page{}
	_, err := e.ExtractResumable(ctx, path, nil, false, func(p Page, _ int) error { pages = append(pages, p); return nil })
	return pages, err
}

func (e PDFExtractor) ExtractResumable(ctx context.Context, path string, completed map[int]bool, force bool, save func(Page, int) error) (int, error) {
	tmp, err := os.MkdirTemp(e.TempRoot, "agenticarchive-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	ctxInfo, cancel := context.WithTimeout(ctx, e.Timeout)
	cmd := exec.CommandContext(ctxInfo, "pdfinfo", path)
	// pdfinfo metadata can be large; keep it on disk instead of capturing it in memory.
	infoPath := filepath.Join(tmp, "info.txt")
	out, err := os.Create(infoPath)
	if err != nil {
		cancel()
		return 0, err
	}
	cmd.Stdout = out
	err = cmd.Run()
	out.Close()
	cancel()
	if err != nil {
		return 0, fmt.Errorf("pdfinfo failed: %w", err)
	}
	info, err := readText(infoPath)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "Pages:") {
			count, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if count < 1 || count > 2000 {
		return 0, fmt.Errorf("unsupported page count: %d (limit 2000)", count)
	}

	totalText := 0
	for n := 1; n <= count; n++ {
		if completed[n] {
			continue
		}
		page := strconv.Itoa(n)
		textPath := filepath.Join(tmp, "page.txt")
		if err = e.command(ctx, "pdftotext", "-f", page, "-l", page, "-enc", "UTF-8", path, textPath); err != nil {
			return 0, fmt.Errorf("page %d: %w", n, err)
		}
		text, err := readText(textPath)
		if err != nil {
			return 0, err
		}
		letters := 0
		for _, r := range text {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		ocr := force || letters < 20
		if ocr {
			imageBase := filepath.Join(tmp, "page")
			imagePath := imageBase + ".png"
			if err = e.command(ctx, "pdftoppm", "-f", page, "-l", page, "-singlefile", "-scale-to", strconv.Itoa(e.MaxDimension), "-gray", "-png", path, imageBase); err != nil {
				return 0, fmt.Errorf("page %d: %w", n, err)
			}
			ocrBase := filepath.Join(tmp, "ocr")
			if err = e.command(ctx, "tesseract", imagePath, ocrBase, "-l", e.Languages); err != nil {
				return 0, fmt.Errorf("page %d: %w", n, err)
			}
			text, err = readText(ocrBase + ".txt")
			if err != nil {
				return 0, err
			}
			os.Remove(imagePath)
			os.Remove(ocrBase + ".txt")
		}
		totalText += len(text)
		if totalText > 16<<20 {
			return 0, fmt.Errorf("document text exceeds 16 MiB limit")
		}
		if err = save(Page{Number: n, Text: text, OCR: ocr}, count); err != nil {
			return count, err
		}
	}
	return count, nil
}
