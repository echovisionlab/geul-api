package mediaruntime

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	meshconfig "github.com/echovisionlab/geul-api/internal/assetprocessing/config"
	meshhandler "github.com/echovisionlab/geul-api/internal/assetprocessing/handler"
	meshjobs "github.com/echovisionlab/geul-api/internal/assetprocessing/jobs"
	meshmq "github.com/echovisionlab/geul-api/internal/assetprocessing/mq"
	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
	meshstorage "github.com/echovisionlab/geul-api/internal/assetprocessing/storage"
	"github.com/echovisionlab/geul-api/internal/assetprocessing/workdir"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	"github.com/echovisionlab/geul-api/internal/transcoding/handler"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobs"
	"github.com/echovisionlab/geul-api/internal/transcoding/mq"
	"github.com/echovisionlab/geul-api/internal/transcoding/storage"
	"github.com/echovisionlab/geul-api/internal/transcoding/waveform"
)

// Workers owns the imported consumers and cancellation listeners with their own
// pools and S3 adapters. Cancel always precedes Close, including partial startup.
type Workers struct {
	group  *Group
	cancel context.CancelFunc
}

func StartWorkers(parent context.Context, cfg *config.Config) (_ *Workers, err error) {
	ctx, cancel := context.WithCancel(parent)
	conn, err := mq.NewConnection(cfg.DatabaseDSN)
	if err != nil {
		cancel()
		return nil, err
	}
	group, err := NewGroup(conn, nil, []Closer{conn})
	if err != nil {
		cancel()
		return nil, err
	}
	runtime := &Workers{group: group, cancel: cancel}
	defer func() {
		if err != nil {
			_ = runtime.Close()
		}
	}()
	if err = configureTranscoding(group, cfg, conn); err != nil {
		return nil, err
	}
	if err = configureWaveform(group, cfg); err != nil {
		return nil, err
	}
	if err = configureMesh(group, cfg); err != nil {
		return nil, err
	}
	if err = group.Start(ctx); err != nil {
		return nil, fmt.Errorf("start media consumers: %w", err)
	}
	return runtime, nil
}

func (w *Workers) Close() error  { w.cancel(); return w.group.Close() }
func (w *Workers) Healthy() bool { return w.group.Healthy() }

func configureTranscoding(group *Group, cfg *config.Config, conn *mq.Connection) error {
	executor, err := ffmpeg.NewExecutor(cfg.Media.FFmpegPath, cfg.Media.FFprobePath, cfg.Media.FFmpegTempDir)
	if err != nil {
		return err
	}
	if _, err := executor.CleanupStaleWorkDirs(); err != nil {
		return err
	}
	store, err := newTranscodingStorage(cfg)
	if err != nil {
		return err
	}
	publisher, err := mq.NewPublisher(conn)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, publisher)
	processor, err := handler.NewHandler(handler.Options{JobTimeoutMinutes: cfg.Media.JobTimeoutMinutes, AudioHLSBitrate: cfg.Media.AudioHLSBitrate, FFmpeg: executor, Storage: store, Publisher: publisher})
	if err != nil {
		return err
	}
	cancelTranscode, err := mq.NewCancelSubscriber(conn, processor.CancelJob)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, cancelTranscode)
	group.AddHealthSource(cancelTranscode)
	queues := []struct {
		config  jobs.QueueConfig
		workers int
		handler mq.Handler
	}{
		{jobs.DefaultAudioQueueConfig(), cfg.Media.AudioWorkers, mq.DecodeAudioJob(processor.HandleAudioJob)},
		{jobs.DefaultVideoQueueConfig(), cfg.Media.VideoWorkers, mq.DecodeVideoJob(processor.HandleVideoJob)},
	}
	for _, queue := range queues {
		queue.config.Workers = queue.workers
		queue.config.Timeout = time.Duration(cfg.Media.JobTimeoutMinutes) * time.Minute
		consumer, err := mq.NewConsumer(conn, queue.config, queue.handler)
		if err != nil {
			return err
		}
		group.closers = append(group.closers, consumer)
		group.starters = append(group.starters, consumer)
	}
	return nil
}

func configureMesh(group *Group, cfg *config.Config) error {
	for _, binary := range []string{cfg.Media.NodePath, cfg.Media.GLTFTransformPath} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("media executable %s: %w", binary, err)
		}
	}
	workDirs, err := workdir.NewManager(cfg.Media.MeshTempDir)
	if err != nil {
		return err
	}
	if _, err := workDirs.CleanupStaleWorkDirs(); err != nil {
		return err
	}
	conn, err := meshmq.NewConnection(cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, conn)
	group.AddHealthSource(conn)
	publisher, err := meshmq.NewPublisher(conn)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, publisher)
	store, err := meshstorage.NewS3Client(&meshconfig.Config{S3Bucket: cfg.S3Bucket, S3Region: cfg.S3Region, S3Endpoint: cfg.S3Endpoint, S3AccessKeyID: cfg.S3AccessKeyID, S3SecretAccessKey: cfg.S3SecretAccessKey, S3ForcePathStyle: cfg.S3ForcePathStyle})
	if err != nil {
		return err
	}
	processor, err := meshhandler.NewProcessor(meshhandler.Config{MaxInputBytes: cfg.Media.MeshMaxInputBytes}, workDirs, store, optimizer.NewCLI(cfg.Media.GLTFTransformPath, cfg.Media.NodePath, cfg.Media.ParticleMeshScriptPath), publisher)
	if err != nil {
		return err
	}
	queue := meshjobs.DefaultMeshQueueConfig()
	queue.Timeout = time.Duration(cfg.Media.MeshTimeoutMinutes) * time.Minute
	consumer, err := meshmq.NewConsumer(conn, queue, processor.HandleMeshJob)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, consumer)
	group.starters = append(group.starters, consumer)
	return nil
}

func newTranscodingStorage(cfg *config.Config) (*storage.S3Client, error) {
	return storage.NewS3Client(storage.Options{Bucket: cfg.S3Bucket, Region: cfg.S3Region, Endpoint: cfg.S3Endpoint, AccessKeyID: cfg.S3AccessKeyID, SecretAccessKey: cfg.S3SecretAccessKey, ForcePathStyle: cfg.S3ForcePathStyle})
}

func configureWaveform(group *Group, cfg *config.Config) error {
	binary := cfg.Media.FFmpegPath
	if cfg.Media.WaveformFFmpegPath != "" {
		binary = cfg.Media.WaveformFFmpegPath
	}
	executor, err := ffmpeg.NewExecutor(binary, cfg.Media.FFprobePath, cfg.Media.WaveformTempDir)
	if err != nil {
		return err
	}
	if _, err := executor.CleanupStaleWorkDirs(); err != nil {
		return err
	}
	conn, err := mq.NewConnection(cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, conn)
	group.AddHealthSource(conn)
	store, err := newTranscodingStorage(cfg)
	if err != nil {
		return err
	}
	publisher, err := mq.NewPublisher(conn)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, publisher)
	processor, err := waveform.NewProcessor(waveform.Options{WorkDirs: executor, Generator: executor, Storage: store, Publisher: publisher})
	if err != nil {
		return err
	}
	subscriber, err := mq.NewWaveformCancelSubscriber(conn, processor.CancelJob)
	if err != nil {
		return err
	}
	group.closers = append(group.closers, subscriber)
	group.AddHealthSource(subscriber)
	queue := jobs.DefaultWaveformQueueConfig()
	queue.Workers = cfg.Media.WaveformWorkers
	queue.Timeout = time.Duration(cfg.Media.JobTimeoutMinutes) * time.Minute
	consumer, err := mq.NewConsumer(conn, queue, mq.DecodeWaveformJob(processor.HandleGenerateJob))
	if err != nil {
		return err
	}
	group.closers = append(group.closers, consumer)
	group.starters = append(group.starters, consumer)
	return nil
}
