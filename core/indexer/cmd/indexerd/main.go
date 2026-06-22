// Command indexerd is the core/indexer service entrypoint: it reads EVM logs at
// the chain head, decodes launchpad events, writes the indexer schema, and fans
// out HMAC-signed webhooks to downstream consumers. All wiring lives in
// internal/app so the binary stays thin.
package main

import (
	"context"
	"log"

	"github.com/Sidiora-Technologies/KindleLaunch/core/indexer/internal/app"
)

func main() {
	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("indexer: %v", err)
	}
}
