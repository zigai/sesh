//go:build linux || darwin

package app

import (
	"context"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/project"
)

func (local *Local) registerProjectHandlers() {
	handle(local, action.ProjectOpen, local.openProject)
	handle(local, action.ProjectClone, local.cloneProject)
	handle(local, action.ProjectCreate, local.createProject)
}

func (local *Local) openProject(ctx context.Context, caller action.Caller, input action.OpenProject) (action.OpenedProject, error) {
	catalog, err := local.projectCatalog()
	if err != nil {
		return emptyOpenedProject(), err
	}

	opened, err := catalog.Resolve(ctx, input.Directory)
	if err != nil {
		return emptyOpenedProject(), err
	}

	return local.enterProject(ctx, caller, opened, input.Adopt)
}

func (local *Local) cloneProject(ctx context.Context, caller action.Caller, input action.CloneProject) (action.OpenedProject, error) {
	catalog, err := local.projectCatalog()
	if err != nil {
		return emptyOpenedProject(), err
	}

	cloned, err := catalog.Clone(ctx, input.URL)
	if err != nil {
		return emptyOpenedProject(), err
	}

	return local.enterProject(ctx, caller, cloned, false)
}

func (local *Local) createProject(ctx context.Context, caller action.Caller, input action.CreateProject) (action.OpenedProject, error) {
	catalog, err := local.projectCatalog()
	if err != nil {
		return emptyOpenedProject(), err
	}

	created, err := catalog.Create(ctx, input.Name)
	if err != nil {
		return emptyOpenedProject(), err
	}

	return local.enterProject(ctx, caller, created, false)
}

// enterProject opens the project's session and moves the invoking client to it.
func (local *Local) enterProject(ctx context.Context, caller action.Caller, p project.Project, adopt bool) (action.OpenedProject, error) {
	result := action.OpenedProject{Root: p.Root, Session: "", Created: false, Adopted: false}

	if local.server == nil {
		return result, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	opened, err := project.Open(ctx, local.server, p, adopt)
	if err != nil {
		return result, oops.In("app").With("operation", "open_project").Wrap(err)
	}

	result.Session = opened.Session.Name
	result.Created = opened.Created
	result.Adopted = opened.Adopted

	if caller.Client != "" && caller.Session != "" {
		_, err = local.workflow.SwitchSessionOutcomeForClient(ctx, opened.Session.Name, caller.Client, caller.Session)
	} else {
		_, err = local.workflow.SwitchSessionOutcome(ctx, opened.Session.Name)
	}

	if err != nil {
		return result, oops.In("app").With("operation", "enter_project").Wrap(err)
	}

	return result, nil
}

func (local *Local) projectCatalog() (project.Catalog, error) {
	catalog, err := project.NewCatalog(local.cfg.Projects)
	if err != nil {
		return project.Catalog{}, action.Wrap(action.CodeInvalidArgument, err)
	}

	return catalog, nil
}

func emptyOpenedProject() action.OpenedProject {
	return action.OpenedProject{Root: "", Session: "", Created: false, Adopted: false}
}
