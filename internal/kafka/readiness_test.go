package kafka

import (
	"context"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/flexprice/flexprice/internal/config"
	"github.com/stretchr/testify/require"
)

func TestKafkaReadinessMetadata(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "healthy", false: "topic unavailable"}[available], func(t *testing.T) {
			broker := sarama.NewMockBroker(t, 1)
			defer broker.Close()
			response := sarama.NewMockMetadataResponse(t).SetBroker(broker.Addr(), 1)
			if available {
				response.SetLeader("events", 0, 1)
			}
			broker.SetHandlerByMap(map[string]sarama.MockResponse{"MetadataRequest": response})
			cfg := &config.KafkaConfig{Brokers: []string{broker.Addr()}, Topic: "events", ClientID: "readiness-test"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := CheckReadiness(ctx, cfg)
			if available {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			history := broker.History()
			require.Len(t, history, 1)
			request, ok := history[0].Request.(*sarama.MetadataRequest)
			require.True(t, ok)
			require.Equal(t, int16(4), request.Version)
			require.False(t, request.AllowAutoTopicCreation)
		})
	}
}

func TestKafkaReadinessCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, CheckReadiness(ctx, &config.KafkaConfig{Brokers: []string{"127.0.0.1:1"}}), context.Canceled)
}
