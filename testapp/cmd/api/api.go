package api

import (
	"fmt"

	"github.com/spf13/cobra"

	"example.com/testapp/internal/mailer"
	"example.com/testapp/internal/user"
)

// API handles the "api" command group (create / list subcommands).
type API struct {
	users  *user.Service
	mailer *mailer.Mailer
}

// NewAPI creates the API command handler with its dependencies.
func NewAPI(users *user.Service, mailer *mailer.Mailer) *API {
	return &API{users: users, mailer: mailer}
}

// Command returns the cobra command tree with subcommands pre-attached.
func (a *API) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Manage users via the HTTP API",
	}
	cmd.AddCommand(
		&cobra.Command{Use: "create", Short: "Create a new user"},
		&cobra.Command{Use: "list", Short: "List all users"},
	)
	return cmd
}

// Create handles the "api create" subcommand.
func (a *API) Create(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if err := a.users.Create(name); err != nil {
		return err
	}
	a.mailer.Notify("admin@example.com", "User created", fmt.Sprintf("New user %q registered.", name))
	return nil
}

// List handles the "api list" subcommand.
func (a *API) List(cmd *cobra.Command) error {
	result, err := a.users.Find("all")
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}
