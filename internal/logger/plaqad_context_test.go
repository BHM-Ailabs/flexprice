package logger

import (
	"context"
	"testing"

	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestPlaqadActorAttributionKeepsNativePrincipal(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	l := &Logger{SugaredLogger: zap.New(core).Sugar()}
	ctx := types.SetUserID(context.Background(), "native-principal")
	ctx = context.WithValue(ctx, types.CtxPlaqadUserID, "central-human")
	l.WithContext(ctx).SugaredLogger.Infow("operation")
	require.Equal(t, 1, logs.Len())
	fields := logs.All()[0].ContextMap()
	assert.Equal(t, "native-principal", fields["user_id"])
	assert.Equal(t, "central-human", fields["plaqad_user_id"])
	assert.NotContains(t, fields, "token")
}
