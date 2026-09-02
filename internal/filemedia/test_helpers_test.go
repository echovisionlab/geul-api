package filemedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/echovisionlab/geul-api/internal/favicon"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type publishedMessage struct {
	exchange     string
	key          string
	mandatory    bool
	immediate    bool
	messageID    string
	deliveryMode uint8
	body         []byte
	messageType  string
}

type capturingAsyncPublisher struct {
	messages               []publishedMessage
	rawPublishCalls        int
	confirmCalls           int
	transactionalExecutors []eventpkg.DBTX
	confirmedErr           error
	rawPublishErr          error
}

func (c *capturingAsyncPublisher) NotifyProtobuf(_ context.Context, signal string, msg proto.Message) error {
	c.rawPublishCalls++
	c.capture(signal, "", "", false, false, msg)
	return c.rawPublishErr
}

func (c *capturingAsyncPublisher) EnqueueProtobuf(_ context.Context, queue, messageID string, msg proto.Message) error {
	c.confirmCalls++
	c.capture("", queue, messageID, true, false, msg)
	return c.confirmedErr
}

func (c *capturingAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	msg proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	c.transactionalExecutors = append(c.transactionalExecutors, executor)
	c.confirmCalls++
	c.capture("", queue, messageID, true, false, msg)
	return c.confirmedErr
}

func (c *capturingAsyncPublisher) capture(exchange, key, messageID string, mandatory, immediate bool, msg proto.Message) {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	c.messages = append(c.messages, publishedMessage{
		exchange: exchange, key: key, mandatory: mandatory, immediate: immediate,
		messageID: messageID, deliveryMode: 2, body: body,
		messageType: string(msg.ProtoReflect().Descriptor().FullName()),
	})
}

func decodePublishedRoutedMessages[T proto.Message](t *testing.T, messages []publishedMessage, exchange, routingKey string, newMessage func() T) []T {
	t.Helper()
	decoded := make([]T, 0)
	for _, message := range messages {
		if message.exchange != exchange || message.key != routingKey {
			continue
		}
		target := newMessage()
		if message.messageType != string(target.ProtoReflect().Descriptor().FullName()) {
			continue
		}
		require.NoError(t, proto.Unmarshal(message.body, target))
		decoded = append(decoded, target)
	}
	return decoded
}

type noopAsyncPublisher struct{}

var errNilTransactionalExecutor = errors.New("transactional fixture executor is required")

func (noopAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errNilTransactionalExecutor
	}
	return nil
}

func faviconTestGeneratedOutputs(t *testing.T) []favicon.Output {
	t.Helper()
	specs := favicon.RequiredOutputs()
	outputs := make([]favicon.Output, 0, len(specs))
	for _, spec := range specs {
		var data []byte
		if spec.MimeType == "image/vnd.microsoft.icon" {
			data = faviconTestICO(t, 16, 32, 48)
		} else {
			data = faviconTestPNG(t, spec.PixelSize, spec.PixelSize, color.NRGBA{R: 200, A: 255})
		}
		outputs = append(outputs, favicon.Output{Spec: spec, Data: data})
	}
	return outputs
}

func faviconTestPNG(t *testing.T, width int, height int, fill color.NRGBA) []byte {
	t.Helper()
	imageData := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			imageData.SetNRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, imageData))
	return buffer.Bytes()
}

func faviconTestICO(t *testing.T, sizes ...int) []byte {
	t.Helper()
	frames := make([][]byte, len(sizes))
	directorySize := 6 + 16*len(sizes)
	totalSize := directorySize
	for index, size := range sizes {
		frames[index] = faviconTestPNG(t, size, size, color.NRGBA{R: uint8(50 + index*40), A: 255})
		totalSize += len(frames[index])
	}
	data := make([]byte, totalSize)
	binary.LittleEndian.PutUint16(data[2:4], 1)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(frames)))
	offset := directorySize
	for index, frame := range frames {
		base := 6 + index*16
		size := sizes[index]
		if size < 256 {
			data[base] = byte(size)
			data[base+1] = byte(size)
		}
		binary.LittleEndian.PutUint16(data[base+4:base+6], 1)
		binary.LittleEndian.PutUint16(data[base+6:base+8], 32)
		binary.LittleEndian.PutUint32(data[base+8:base+12], uint32(len(frame)))
		binary.LittleEndian.PutUint32(data[base+12:base+16], uint32(offset))
		copy(data[offset:], frame)
		offset += len(frame)
	}
	return data
}

var _ TransactionalAsyncPublisher = noopAsyncPublisher{}

func newServiceUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
			id text PRIMARY KEY, file_name text, mime_type text, file_size integer,
			extension text, sha256 blob, duration_seconds integer,
			ingest_slot_id text, ingest_attempt_id text,
			delete_requested_at datetime, created_at datetime
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset (
			id text PRIMARY KEY, source_file_id text, kind text NOT NULL,
			object_key text NOT NULL UNIQUE, extension text NOT NULL, mime_type text NOT NULL,
			file_size integer, sha256 blob, disposition text NOT NULL, download_filename text,
			status text NOT NULL, ready_at datetime, delete_requested_at datetime, deleted_at datetime,
			failed_at datetime, failure_reason text, created_at datetime NOT NULL, updated_at datetime NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset_binding (
			asset_id text NOT NULL, owner_type text NOT NULL, owner_id text NOT NULL,
			binding_key text NOT NULL, source_file_id text, created_at datetime NOT NULL,
			updated_at datetime NOT NULL, PRIMARY KEY (owner_type, owner_id, binding_key)
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE media_generation (
			id text PRIMARY KEY, file_id text NOT NULL, kind text NOT NULL,
			object_prefix text NOT NULL UNIQUE, manifest_name text NOT NULL,
			manifest_sha256 blob, object_count integer, total_size integer, status text NOT NULL,
			ready_at datetime, retired_at datetime, delete_after datetime,
			created_at datetime NOT NULL, updated_at datetime NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file_ingest_binding (
			file_id text PRIMARY KEY, upload_type text NOT NULL, entity_type text,
			entity_id text NOT NULL, created_at datetime NOT NULL
		)
	`).Error)
	return db
}
