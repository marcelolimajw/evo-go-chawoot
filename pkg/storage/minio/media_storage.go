package minio_storage

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	storage_interfaces "github.com/EvolutionAPI/evolution-go/pkg/storage/interfaces"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinioMediaStorage struct {
	client     *minio.Client
	bucketName string
	baseURL    string
}

// generateFilePath creates a simple media folder structure
// Format: evolution-go-medias/{filename}
func generateFilePath(fileName string) string {
	return fmt.Sprintf("evolution-go-medias/%s", fileName)
}

// resolveFilePath determines if the input is a full path or just a filename
// If it's just a filename, it assumes it's in the evolution-go-medias folder
// If it's a full path, it returns it as-is
func (m *MinioMediaStorage) resolveFilePath(ctx context.Context, fileNameOrPath string) (string, error) {
	// If the input already contains path separators, assume it's a full path
	if strings.Contains(fileNameOrPath, "/") {
		return fileNameOrPath, nil
	}

	// If it's just a filename, assume it's in the evolution-go-medias folder
	return fmt.Sprintf("evolution-go-medias/%s", fileNameOrPath), nil
}

func NewMinioMediaStorage(
	endpoint,
	accessKeyID,
	secretAccessKey,
	bucketName,
	region string,
	useSSL bool,
) (storage_interfaces.MediaStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MinIO client: %w", err)
	}

	baseURL := fmt.Sprintf("https://%s/%s", endpoint, bucketName)
	if !useSSL {
		baseURL = fmt.Sprintf("http://%s/%s", endpoint, bucketName)
	}

	return &MinioMediaStorage{
		client:     client,
		bucketName: bucketName,
		baseURL:    baseURL,
	}, nil
}

func (m *MinioMediaStorage) Store(ctx context.Context, data []byte, fileName string, contentType string) (string, error) {
	// Generate organized file path
	filePath := generateFilePath(fileName)
	reader := bytes.NewReader(data)

	_, err := m.client.PutObject(ctx, m.bucketName, filePath, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("failed to store object: %w", err)
	}

	// Construindo URL permanente (pública)
	permanentURL := fmt.Sprintf("%s/%s", m.baseURL, filePath)
	fmt.Println(permanentURL)

	return permanentURL, nil
}

func (m *MinioMediaStorage) Delete(ctx context.Context, fileName string) error {
	// Resolve the full path for the file
	filePath, err := m.resolveFilePath(ctx, fileName)
	if err != nil {
		return fmt.Errorf("failed to resolve file path: %w", err)
	}

	err = m.client.RemoveObject(ctx, m.bucketName, filePath, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

func (m *MinioMediaStorage) GetURL(ctx context.Context, fileName string) (string, error) {
	// Resolve the full path for the file
	filePath, err := m.resolveFilePath(ctx, fileName)
	if err != nil {
		return "", fmt.Errorf("failed to resolve file path: %w", err)
	}

	// Check if object exists
	_, err = m.client.StatObject(ctx, m.bucketName, filePath, minio.StatObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get object stats: %w", err)
	}

	// Construindo URL permanente (pública)
	permanentURL := fmt.Sprintf("%s/%s", m.baseURL, filePath)
	fmt.Println(permanentURL)

	return permanentURL, nil
}

func (m *MinioMediaStorage) DeleteByPrefix(ctx context.Context, prefix string) error {
	// Ensure the prefix includes the main folder
	fullPrefix := fmt.Sprintf("evolution-go-medias/%s", prefix)

	objectsCh := make(chan minio.ObjectInfo)

	// List objects with prefix
	go func() {
		defer close(objectsCh)
		for object := range m.client.ListObjects(ctx, m.bucketName, minio.ListObjectsOptions{Prefix: fullPrefix, Recursive: true}) {
			if object.Err != nil {
				return
			}
			objectsCh <- object
		}
	}()

	// Delete found objects
	errorCh := m.client.RemoveObjects(ctx, m.bucketName, objectsCh, minio.RemoveObjectsOptions{})
	for err := range errorCh {
		if err.Err != nil {
			return fmt.Errorf("failed to delete object %s: %w", err.ObjectName, err.Err)
		}
	}

	return nil
}
