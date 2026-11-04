package executor

import (
	"context"
	"sync"
	"time"
)

// Pi only exposes levels for its selected model. Warm a bounded directory once,
// using separate --no-session processes, never persistent set_model commands.
func resolvePiCatalog(ctx context.Context, execPath, workDir, model string) (CLIModelCatalog, error) {
	if model != "" {
		return resolvePiSelectedCatalog(ctx, execPath, workDir, model)
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	catalog, err := resolvePiSelectedCatalog(ctx, execPath, workDir, "")
	if err != nil {
		return catalog, err
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				next, probeErr := resolvePiSelectedCatalog(probeCtx, execPath, workDir, catalog.Options[index].ID)
				stop()
				if probeErr == nil {
					if option := catalogOption(next, CLITypePi, catalog.Options[index].ID); option != nil {
						catalog.Options[index].Thinking = option.Thinking
					}
				}
			}
		}()
	}
	limit := min(len(catalog.Options), 128)
feed:
	for index := 0; index < limit; index++ {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	for _, option := range catalog.Options {
		if option.Thinking.State == "fresh" {
			return catalog, nil
		}
	}
	return catalog, ErrThinkingUnavailable
}
