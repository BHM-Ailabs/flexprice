package kafka

import (
	"context"
	"fmt"
	"time"

	"github.com/Shopify/sarama"
	"github.com/flexprice/flexprice/internal/config"
)

// CheckReadiness requests metadata without creating a topic or publishing an event.
func CheckReadiness(ctx context.Context, cfg *config.KafkaConfig) error {
	if len(cfg.Brokers) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}
	conf, err := GetSaramaConfig(cfg)
	if err != nil {
		return err
	}
	conf.Net.DialTimeout = 500 * time.Millisecond
	conf.Net.ReadTimeout = 500 * time.Millisecond
	conf.Net.WriteTimeout = 500 * time.Millisecond
	for _, address := range cfg.Brokers {
		if err := ctx.Err(); err != nil {
			return err
		}
		broker := sarama.NewBroker(address)
		if err := broker.Open(conf); err != nil {
			continue
		}
		stop := context.AfterFunc(ctx, func() { _ = broker.Close() })
		response, err := broker.GetMetadata(&sarama.MetadataRequest{Version: 4, Topics: []string{cfg.Topic}, AllowAutoTopicCreation: false})
		stop()
		_ = broker.Close()
		if err == nil && response != nil && len(response.Brokers) > 0 {
			healthy := false
			for _, topic := range response.Topics {
				if topic.Name == cfg.Topic {
					healthy = topic.Err == sarama.ErrNoError && len(topic.Partitions) > 0
					for _, partition := range topic.Partitions {
						if partition.Err != sarama.ErrNoError || partition.Leader < 0 {
							healthy = false
						}
					}
				}
			}
			if healthy {
				return nil
			}
		}
	}
	return fmt.Errorf("kafka unavailable")
}
