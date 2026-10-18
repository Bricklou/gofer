package views

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/a-h/templ"
	"github.com/cristianadrielbraun/gofer/internal/models"
)

// An account imported from a profile waits for logins before its services
// sync: its mail password, and a contacts or calendar password when those
// had their own. Gmail and Outlook wait for a new sign-in.
const (
	reconnectMail     = "mail"
	reconnectContacts = "contacts"
	reconnectCalendar = "calendar"
	reconnectSignIn   = "signin"
)

type reconnectMarksKey struct{}

// withReconnectMarks renders the edit wizard's steps with a mark on each step
// where the imported account still needs a password.
func withReconnectMarks(data models.EditAccountData, component templ.Component) templ.Component {
	marks := map[int]string{}
	if slices.Contains(data.ReconnectNeeds, reconnectMail) {
		marks[1] = reconnectMail
		if data.ReconnectSMTP {
			marks[2] = reconnectMail
		}
	}
	if slices.Contains(data.ReconnectNeeds, reconnectContacts) {
		marks[4] = reconnectContacts
	}
	if slices.Contains(data.ReconnectNeeds, reconnectCalendar) {
		marks[5] = reconnectCalendar
	}
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return component.Render(context.WithValue(ctx, reconnectMarksKey{}, marks), w)
	})
}

// reconnectMark is the login a wizard step still needs, if any.
func reconnectMark(ctx context.Context, step int) string {
	marks, _ := ctx.Value(reconnectMarksKey{}).(map[int]string)
	return marks[step]
}

func editAccountNeeds(data models.EditAccountData, login string) bool {
	return slices.Contains(data.ReconnectNeeds, login)
}

func reconnectLoginLabel(login string) string {
	switch login {
	case reconnectContacts:
		return "Contacts"
	case reconnectCalendar:
		return "Calendar"
	default:
		return "Mail"
	}
}

func reconnectLoginLabels(needs []string) string {
	labels := make([]string, 0, len(needs))
	for _, login := range needs {
		labels = append(labels, reconnectLoginLabel(login))
	}
	return joinWithAnd(labels)
}

func accountReconnectPendingMessage(account models.Account) string {
	needs := account.ReconnectNeeds
	switch {
	case slices.Contains(needs, reconnectSignIn):
		return "Imported from a profile. Sign in again to start syncing."
	case len(needs) == 1 && needs[0] == reconnectMail:
		return "Imported from a profile. Enter its password to start syncing."
	case slices.Contains(needs, reconnectMail):
		return "Imported from a profile. Enter its passwords for " + reconnectLoginLabels(needs) + " to start syncing."
	case len(needs) == 1:
		return reconnectLoginLabel(needs[0]) + " still needs its password."
	default:
		return reconnectLoginLabels(needs) + " still need their passwords."
	}
}

func accountReconnectButtonLabel(account models.Account) string {
	if len(account.ReconnectNeeds) > 1 {
		return "Enter passwords"
	}
	return "Enter password"
}

// editAccountReconnectSteps names where each missing password goes, for the
// note at the top of the edit dialog.
func editAccountReconnectSteps(data models.EditAccountData) []string {
	var steps []string
	for _, login := range data.ReconnectNeeds {
		switch login {
		case reconnectMail:
			if editAccountIsICloud(data) {
				steps = append(steps, "Mail sync")
			} else if data.ReconnectSMTP {
				steps = append(steps, "Incoming and Outgoing")
			} else {
				steps = append(steps, "Incoming")
			}
		case reconnectContacts:
			steps = append(steps, "Contacts")
		case reconnectCalendar:
			steps = append(steps, "Calendar")
		}
	}
	return steps
}

func editAccountReconnectNote(data models.EditAccountData) string {
	steps := editAccountReconnectSteps(data)
	if len(steps) <= 1 && editAccountNeeds(data, reconnectMail) {
		return "This account was imported from a profile without its password. Enter it in " + strings.Join(steps, "") + ", marked with !, then test and save to start syncing."
	}
	return "This account was imported from a profile without its passwords. Enter them in the steps marked with ! (" + joinWithAnd(steps) + "), in any order. Each service starts syncing once its password is saved."
}

func reconnectRequiredAttrs(login string, required bool) templ.Attributes {
	if !required {
		return nil
	}
	return templ.Attributes{"data-reconnect-required": login}
}

// reconnectGateAttrs keep a save button unusable until the imported
// account's password fields in form are filled. When service names a
// section switch, the button is only held while that service is on.
func reconnectGateAttrs(form, service string, gated bool) templ.Attributes {
	if !gated {
		return nil
	}
	attrs := templ.Attributes{"data-reconnect-gate": form}
	if service != "" {
		attrs["data-reconnect-gate-switch"] = service
	}
	return attrs
}
