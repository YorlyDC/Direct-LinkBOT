package utils

import (
	"fmt"
	"encoding/base64"
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/types"
)

func PackFile(fileName string, fileSize int64, mimeType string, fileID int64) string {
	data := fmt.Sprintf("%s|%d|%s|%d", fileName, fileSize, mimeType, fileID)
	return base64.URLEncoding.EncodeToString([]byte(data))
}

func GetShortHash(fullHash string) string {
	return fullHash[:config.ValueOf.HashLength]
}

func CheckHash(inputHash string, expectedHash string) bool {
	return inputHash == GetShortHash(expectedHash)
}
