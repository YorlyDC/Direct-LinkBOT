package routes

import (
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/utils"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	range_parser "github.com/quantumsheep/range-parser"
	"go.uber.org/zap"

	"github.com/gin-gonic/gin"
	"encoding/base64"
)

var log *zap.Logger

func (e *allRoutes) LoadHome(r *Route) {
	log = e.log.Named("Stream")
	defer log.Info("Loaded stream route")
	r.Engine.GET("/stream/:messageID", getStreamRoute)
}

func getStreamRoute(ctx *gin.Context) {
	w := ctx.Writer
	r := ctx.Request

	messageIDParm := ctx.Param("messageID")
	messageID, err := strconv.Atoi(messageIDParm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	authHash := ctx.Query("hash")
	if authHash == "" {
		http.Error(w, "missing hash param", http.StatusBadRequest)
		return
	}

	ctx.Header("Accept-Ranges", "bytes")
	var start, end int64
	rangeHeader := r.Header.Get("Range")

	worker := bot.GetNextWorker()

	file, err := utils.FileFromMessage(ctx, worker.Client, messageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	expectedHash := utils.PackFile(
		file.FileName,
		file.FileSize,
		file.MimeType,
		file.ID,
	)
	if !utils.CheckHash(authHash, expectedHash) {
		log.Error("Invalid hash", zap.String("authHash", authHash), zap.String("expectedHash", expectedHash))
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}

	if rangeHeader == "" {
		start = 0
		end = file.FileSize - 1
		w.WriteHeader(http.StatusOK)
	} else {
		ranges, err := range_parser.Parse(file.FileSize, r.Header.Get("Range"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		start = ranges[0].Start
		end = ranges[0].End
		ctx.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, file.FileSize))
		log.Info("Content-Range", zap.Int64("start", start), zap.Int64("end", end), zap.Int64("fileSize", file.FileSize))
		w.WriteHeader(http.StatusPartialContent)
	}

	contentLength := end - start + 1
	mimeType := file.MimeType

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	ctx.Header("Content-Type", mimeType)
	ctx.Header("Content-Length", strconv.FormatInt(contentLength, 10))

	disposition := "inline"

	if ctx.Query("d") == "true" {
		disposition = "attachment"
	}

	ctx.Header("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, file.FileName))

	if r.Method != "HEAD" {
		lr, err := utils.NewTelegramReader(ctx, worker.Client, file.Location, start, end, contentLength, utils.Config{
			BufferSize: 512 * 1024, // 512KB buffer
			CacheTTL:   10 * time.Minute,
			CacheSize:  100 * 1024 * 1024, // 100MB cache
		})
		if err != nil {
			log.Error("Error creating TelegramReader", zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		defer lr.Close()

		buf := make([]byte, 32*1024)
		for {
			select {
			case <-ctx.Request.Context().Done():
				log.Info("Client closed connection")
				return
			default:
				n, err := lr.Read(buf)
				if err != nil {
					if err == io.EOF {
						return
					}
					log.Error("Error reading from TelegramReader", zap.Error(err))
					return
				}
				if n == 0 {
					continue
				}
				_, err = w.Write(buf[:n])
				if err != nil {
					if err != http.ErrHandlerTimeout && !strings.Contains(err.Error(), "broken pipe") && !strings.Contains(err.Error(), "connection reset by peer") {
						log.Error("Error writing to response", zap.Error(err))
					}
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
}

func PackFile(fileName string, fileSize int64, mimeType string, fileID int64) string {
	data := fmt.Sprintf("%s|%d|%s|%d", fileName, fileSize, mimeType, fileID)
	return base64.URLEncoding.EncodeToString([]byte(data))
}

