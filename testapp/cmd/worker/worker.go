package worker

import (
	"fmt"

	"github.com/spf13/cobra"

	"example.com/testapp/internal/mailer"
)

// Worker processes background jobs.
type Worker struct {
	mailer *mailer.Mailer
}

// NewWorker creates the worker command handler.
func NewWorker(mailer *mailer.Mailer) *Worker {
	return &Worker{mailer: mailer}
}

// Command returns the cobra command for the worker.
func (w *Worker) Command() *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "Run the background job worker",
	}
}

// Handle is the single entry point for the worker command.
func (w *Worker) Handle(cmd *cobra.Command) error {
	fmt.Println("worker: starting background job loop")
	w.mailer.Notify("ops@example.com", "Worker started", "Background worker is now running.")
	return nil
}
