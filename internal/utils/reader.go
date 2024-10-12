package utils

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
	"github.com/coocood/freecache"
)

const (
	defaultBufferSize = 512 * 1024  // 512KB default buffer size
	maxBufferSize     = 1024 * 1024 // 1MB max buffer size
	defaultCacheTTL   = 10 * time.Minute
	maxCacheSize      = 100 * 1024 * 1024 // 100MB max cache size
	maxActiveReaders  = 35
)

var (
	cache          *freecache.Cache
	activeReaders  int
	readersMutex   sync.Mutex
)

type Config struct {
	BufferSize int
	CacheTTL   time.Duration
	CacheSize  int
}

type telegramReader struct {
	ctx           context.Context
	log           *zap.Logger
	client        *gotgproto.Client
	location      *tg.InputDocumentFileLocation
	start         int64
	end           int64
	next          func() ([]byte, error)
	buffer        []byte
	bytesread     int64
	chunkSize     int64
	i             int64
	contentLength int64
	mu            sync.Mutex
	config        Config
}

func NewTelegramReader(
	ctx context.Context,
	client *gotgproto.Client,
	location *tg.InputDocumentFileLocation,
	start int64,
	end int64,
	contentLength int64,
	config Config,
) (io.ReadCloser, error) {
	readersMutex.Lock()
	defer readersMutex.Unlock()

	if activeReaders >= maxActiveReaders {
		return nil, fmt.Errorf("maximum number of active readers reached")
	}
	activeReaders++

	if config.BufferSize <= 0 || config.BufferSize > maxBufferSize {
		config.BufferSize = defaultBufferSize
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = defaultCacheTTL
	}
	if config.CacheSize <= 0 || config.CacheSize > maxCacheSize {
		config.CacheSize = maxCacheSize
	}

	if cache == nil || cache.Size() != config.CacheSize {
		cache = freecache.NewCache(config.CacheSize)
	}

	r := &telegramReader{
		ctx:           ctx,
		log:           Logger.Named("telegramReader"),
		location:      location,
		client:        client,
		start:         start,
		end:           end,
		chunkSize:     int64(config.BufferSize),
		contentLength: contentLength,
		config:        config,
		buffer:        make([]byte, config.BufferSize),
	}
	r.log.Sugar().Debugf("TelegramReader initialized with BufferSize: %d, CacheTTL: %v, CacheSize: %d", 
		config.BufferSize, config.CacheTTL, config.CacheSize)
	r.next = r.partStream()
	return r, nil
}

func (r *telegramReader) Read(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.bytesread == r.contentLength {
		r.log.Sugar().Debug("EOF (bytesread == contentLength)")
		return 0, io.EOF
	}

	if r.i >= int64(len(r.buffer)) {
		r.buffer, err = r.next()
		r.log.Debug("Next Buffer", zap.Int64("len", int64(len(r.buffer))))
		if err != nil {
			return 0, err
		}
		if len(r.buffer) == 0 {
			r.next = r.partStream()
			r.buffer, err = r.next()
			if err != nil {
				return 0, err
			}
		}
		r.i = 0
	}
	n = copy(p, r.buffer[r.i:])
	r.i += int64(n)
	r.bytesread += int64(n)
	return n, nil
}

func (r *telegramReader) chunk(offset int64, limit int64) ([]byte, error) {
	cacheKey := fmt.Sprintf("%d:%d:%d", r.location.ID, offset, limit)
	cachedData, err := cache.Get([]byte(cacheKey))
	if err == nil {
		return cachedData, nil
	}

	req := &tg.UploadGetFileRequest{
		Offset:   offset,
		Limit:    int(limit),
		Location: r.location,
	}

	res, err := r.client.API().UploadGetFile(r.ctx, req)
	if err != nil {
		return nil, err
	}

	switch result := res.(type) {
	case *tg.UploadFile:
		cache.Set([]byte(cacheKey), result.Bytes, int(r.config.CacheTTL.Seconds()))
		return result.Bytes, nil
	default:
		return nil, fmt.Errorf("unexpected type %T", r)
	}
}

func (r *telegramReader) partStream() func() ([]byte, error) {
	start := r.start
	end := r.end
	offset := start - (start % r.chunkSize)

	firstPartCut := start - offset
	lastPartCut := (end % r.chunkSize) + 1
	partCount := int((end - offset + r.chunkSize) / r.chunkSize)
	currentPart := 1

	readData := func() ([]byte, error) {
		if currentPart > partCount {
			return make([]byte, 0), nil
		}
		res, err := r.chunk(offset, r.chunkSize)
		if err != nil {
			return nil, err
		}
		if len(res) == 0 {
			return res, nil
		} else if partCount == 1 {
			res = res[firstPartCut:lastPartCut]
		} else if currentPart == 1 {
			res = res[firstPartCut:]
		} else if currentPart == partCount {
			res = res[:lastPartCut]
		}

		currentPart++
		offset += r.chunkSize
		r.log.Sugar().Debugf("Part %d/%d", currentPart, partCount)
		return res, nil
	}
	return readData
}

func (r *telegramReader) Close() error {
	readersMutex.Lock()
	defer readersMutex.Unlock()
	activeReaders--
	return nil
}
