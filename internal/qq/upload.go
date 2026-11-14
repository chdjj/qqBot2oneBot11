package qq

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// md5Window is the prefix length the platform hashes for md5_10m
// (10002432 bytes ≈ 9.54 MiB, documented as "about 10MB").
const md5Window = 10002432

// UploadPart is one chunk to be PUT to a presigned URL.
type UploadPart struct {
	Index        int    `json:"index"`
	PresignedURL string `json:"presigned_url"`
	BlockSize    string `json:"block_size"`
}

// UploadConfig is the server-side pacing guidance for a chunked upload.
type UploadConfig struct {
	Concurrency  int `json:"concurrency"`
	RetryTimeout int `json:"retry_timeout"`
	RetryDelay   int `json:"retry_delay"`
}

// UploadPrepare is the response of the chunked-upload handshake.
type UploadPrepare struct {
	UploadID     string       `json:"upload_id"`
	BlockSize    string       `json:"block_size"`
	Parts        []UploadPart `json:"parts"`
	UploadConfig UploadConfig `json:"upload_config"`
}

// prepareRequest bodies the platform requires to describe a file.
type prepareRequest struct {
	FileType int    `json:"file_type"`
	FileSize string `json:"file_size"`
	FileName string `json:"file_name"`
	MD5      string `json:"md5"`
	SHA1     string `json:"sha1"`
	MD5TenM  string `json:"md5_10m"`
}

// scopePath builds the per-recipient prefix: groups or users.
func scopePath(kind, openID string) string {
	if kind == "private" {
		return "/v2/users/" + url.PathEscape(openID)
	}
	return "/v2/groups/" + url.PathEscape(openID)
}

// hashes computes the three digests the upload handshake expects.
func hashes(data []byte) (md5Hex, sha1Hex, md5TenM string) {
	sum := md5.Sum(data)
	md5Hex = hex.EncodeToString(sum[:])

	s := sha1.Sum(data)
	sha1Hex = hex.EncodeToString(s[:])

	prefix := data
	if len(prefix) > md5Window {
		prefix = prefix[:md5Window]
	}
	p := md5.Sum(prefix)
	md5TenM = hex.EncodeToString(p[:])
	return md5Hex, sha1Hex, md5TenM
}

// UploadChunked uploads a file too large for direct upload by splitting it into
// blocks and PUTting each to a presigned URL, confirming every part, then asking
// the platform to assemble them.
func (c *Client) UploadChunked(ctx context.Context, kind, openID string, m MediaUpload, data []byte) (*UploadResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("分片上传的文件为空")
	}
	md5Hex, sha1Hex, md5TenM := hashes(data)

	req := prepareRequest{
		FileType: m.FileType,
		FileSize: strconv.Itoa(len(data)),
		FileName: m.Name,
		MD5:      md5Hex,
		SHA1:     sha1Hex,
		MD5TenM:  md5TenM,
	}
	var prep UploadPrepare
	if err := c.Do(ctx, http.MethodPost, scopePath(kind, openID)+"/upload_prepare", req, &prep); err != nil {
		return nil, fmt.Errorf("预上传失败: %w", err)
	}
	if prep.UploadID == "" || len(prep.Parts) == 0 {
		return nil, fmt.Errorf("预上传返回为空")
	}

	blockSize := atoiOr(prep.BlockSize, len(data))

	// Honour the server's pacing advice rather than hammering the URLs.
	parallel := prep.UploadConfig.Concurrency
	if parallel < 1 {
		parallel = 1
	}
	retryDelay := time.Duration(prep.UploadConfig.RetryDelay) * time.Second
	if retryDelay <= 0 {
		retryDelay = time.Second
	}

	for _, part := range prep.Parts {
		chunk, ok := sliceForPart(data, part, blockSize)
		if !ok {
			return nil, fmt.Errorf("分片 %d 超出文件范围", part.Index)
		}
		if err := c.putPart(ctx, part.PresignedURL, chunk, retryDelay); err != nil {
			return nil, fmt.Errorf("上传分片 %d 失败: %w", part.Index, err)
		}
		partMD5 := md5.Sum(chunk)
		body := map[string]any{
			"upload_id":  prep.UploadID,
			"part_index": part.Index,
			"block_size": strconv.Itoa(len(chunk)),
			"md5":        hex.EncodeToString(partMD5[:]),
		}
		if err := c.Do(ctx, http.MethodPost, scopePath(kind, openID)+"/upload_part_finish", body, nil); err != nil {
			return nil, fmt.Errorf("确认分片 %d 失败: %w", part.Index, err)
		}
	}
	_ = parallel

	// Final call merges the parts; url must be absent in this mode.
	final := map[string]any{
		"file_type":    m.FileType,
		"srv_send_msg": false,
		"file_name":    m.Name,
		"upload_id":    prep.UploadID,
	}
	var out UploadResult
	if err := c.Do(ctx, http.MethodPost, scopePath(kind, openID)+"/files", final, &out); err != nil {
		return nil, fmt.Errorf("合并分片失败: %w", err)
	}
	if out.FileInfo == "" {
		return nil, fmt.Errorf("合并成功但未返回 file_info")
	}
	return &out, nil
}

// sliceForPart returns the bytes belonging to one part.
func sliceForPart(data []byte, part UploadPart, fallback int) ([]byte, bool) {
	size := atoiOr(part.BlockSize, fallback)
	start := part.Index * size
	// Trust the server's per-part size when present; the last part is shorter.
	if start < 0 || start > len(data) {
		return nil, false
	}
	end := start + size
	if end > len(data) {
		end = len(data)
	}
	if end <= start {
		return nil, false
	}
	return data[start:end], true
}

// putPart uploads one chunk with a couple of retries; presigned uploads are
// idempotent, so retrying is safe.
func (c *Client) putPart(ctx context.Context, presigned string, chunk []byte, delay time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, presigned, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		// Presigned COS URLs reject a Content-Type header mismatch.
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(chunk))
		hc := c.httpClient()
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return lastErr
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// rawJSON is used in tests to assert on request bodies.
var _ = json.Marshal
