// Copyright 2012 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package provision provides interfaces that need to be satisfied in order to
// implement a new provisioner on tsuru.
package provision

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/pkg/errors"
	"github.com/tsuru/tsuru/app/bind"
	"github.com/tsuru/tsuru/event"
	"github.com/tsuru/tsuru/router"
)

const defaultDockerProvisioner = "docker"

var (
	ErrInvalidStatus = errors.New("invalid status")
	ErrEmptyApp      = errors.New("no units for this app")
	ErrNodeNotFound  = errors.New("node not found")

	DefaultProvisioner = defaultDockerProvisioner
)

type UnitNotFoundError struct {
	ID string
}

func (e *UnitNotFoundError) Error() string {
	return fmt.Sprintf("unit %q not found", e.ID)
}

type InvalidProcessError struct {
	Msg string
}

func (e InvalidProcessError) Error() string {
	return fmt.Sprintf("process error: %s", e.Msg)
}

type ProvisionerNotSupported struct {
	Prov   Provisioner
	Action string
}

func (e ProvisionerNotSupported) Error() string {
	return fmt.Sprintf("provisioner %q does not support %s", e.Prov.GetName(), e.Action)
}

// Status represents the status of a unit in tsuru.
type Status string

func (s Status) String() string {
	return string(s)
}

func ParseStatus(status string) (Status, error) {
	switch status {
	case "created":
		return StatusCreated, nil
	case "building":
		return StatusBuilding, nil
	case "error":
		return StatusError, nil
	case "started":
		return StatusStarted, nil
	case "starting":
		return StatusStarting, nil
	case "stopped":
		return StatusStopped, nil
	case "asleep":
		return StatusAsleep, nil
	}
	return Status(""), ErrInvalidStatus
}

//     Flow:
//                                    +----------------------------------------------+
//                                    |                                              |
//                                    |            Start                             |
//     +----------+                   |                      +---------+             |
//     | Building |                   +---------------------+| Stopped |             |
//     +----------+                   |                      +---------+             |
//           ^                        |                           ^                  |
//           |                        |                           |                  |
//      deploy unit                   |                         Stop                 |
//           |                        |                           |                  |
//           +                        v       RegisterUnit        +                  +
//      +---------+  app unit   +----------+  SetUnitStatus  +---------+  Sleep  +--------+
//      | Created | +---------> | Starting | +-------------> | Started |+------->| Asleep |
//      +---------+             +----------+                 +---------+         +--------+
//                                    +                         ^ +
//                                    |                         | |
//                              SetUnitStatus                   | |
//                                    |                         | |
//                                    v                         | |
//                                +-------+     SetUnitStatus   | |
//                                | Error | +-------------------+ |
//                                +-------+ <---------------------+
const (
	// StatusCreated is the initial status of a unit in the database,
	// it should transition shortly to a more specific status
	StatusCreated = Status("created")

	// StatusBuilding is the status for units being provisioned by the
	// provisioner, like in the deployment.
	StatusBuilding = Status("building")

	// StatusError is the status for units that failed to start, because of
	// an application error.
	StatusError = Status("error")

	// StatusStarting is set when the container is started in docker.
	StatusStarting = Status("starting")

	// StatusStarted is for cases where the unit is up and running, and bound
	// to the proper status, it's set by RegisterUnit and SetUnitStatus.
	StatusStarted = Status("started")

	// StatusStopped is for cases where the unit has been stopped.
	StatusStopped = Status("stopped")

	// StatusAsleep is for cases where the unit has been asleep.
	StatusAsleep = Status("asleep")
)

// Unit represents a provision unit. Can be a machine, container or anything
// IP-addressable.
type Unit struct {
	ID          string
	Name        string
	AppName     string
	ProcessName string
	Type        string
	IP          string
	Status      Status
	Address     *url.URL
}

// GetName returns the name of the unit.
func (u *Unit) GetID() string {
	return u.ID
}

// GetIp returns the Unit.IP.
func (u *Unit) GetIp() string {
	return u.IP
}

func (u *Unit) MarshalJSON() ([]byte, error) {
	type UnitForMarshal Unit
	host, port, _ := net.SplitHostPort(u.Address.Host)
	// New fields added for compatibility with old routes returning containers.
	return json.Marshal(&struct {
		*UnitForMarshal
		HostAddr string
		HostPort string
		IP       string
	}{
		UnitForMarshal: (*UnitForMarshal)(u),
		HostAddr:       host,
		HostPort:       port,
		IP:             u.IP,
	})
}

// Available returns true if the unit is available. It will return true
// whenever the unit itself is available, even when the application process is
// not.
func (u *Unit) Available() bool {
	return u.Status == StatusStarted ||
		u.Status == StatusStarting ||
		u.Status == StatusError
}

// Named is something that has a name, providing the GetName method.
type Named interface {
	GetName() string
}

// RunArgs groups together the arguments to run an App.
type RunArgs struct {
	Once     bool
	Isolated bool
}

// App represents a tsuru app.
//
// It contains only relevant information for provisioning.
type App interface {
	Named

	BindUnit(*Unit) error
	UnbindUnit(*Unit) error

	// GetPlatform returns the platform (type) of the app. It is equivalent
	// to the Unit `Type` field.
	GetPlatform() string

	// GetDeploy returns the deploys that an app has.
	GetDeploys() uint

	Envs() map[string]bind.EnvVar

	GetMemory() int64
	GetSwap() int64
	GetCpuShare() int

	SetUpdatePlatform(bool) error
	GetUpdatePlatform() bool

	GetRouterName() (string, error)

	GetPool() string

	SetQuotaInUse(int) error
}

type AppLock interface {
	json.Marshaler

	GetLocked() bool

	GetReason() string

	GetOwner() string

	GetAcquireDate() time.Time
}

// ShellOptions is the set of options that can be used when calling the method
// Shell in the provisioner.
type ShellOptions struct {
	App    App
	Conn   io.ReadWriteCloser
	Width  int
	Height int
	Unit   string
	Term   string
}

// ArchiveDeployer is a provisioner that can deploy archives.
type ArchiveDeployer interface {
	ArchiveDeploy(app App, archiveURL string, evt *event.Event) (string, error)
}

// UploadDeployer is a provisioner that can deploy the application from an
// uploaded file.
type UploadDeployer interface {
	UploadDeploy(app App, file io.ReadCloser, fileSize int64, build bool, evt *event.Event) (string, error)
}

// ImageDeployer is a provisioner that can deploy the application from a
// previously generated image.
type ImageDeployer interface {
	ImageDeploy(app App, image string, evt *event.Event) (string, error)
}

// RollbackableDeployer is a provisioner that allows rolling back to a
// previously deployed version.
type RollbackableDeployer interface {
	Rollback(App, string, *event.Event) (string, error)
}

// RebuildableDeployer is a provisioner that allows rebuild the last
// deployed image.
type RebuildableDeployer interface {
	Rebuild(App, *event.Event) (string, error)
}

// BuilderDeploy is a provisioner that allows deploy builded image.
type BuilderDeploy interface {
	Deploy(App, string, *event.Event) (string, error)
	GetDockerClient(App) (*docker.Client, error)
	CleanImage(appName string, image string)
}

// Provisioner is the basic interface of this package.
//
// Any tsuru provisioner must implement this interface in order to provision
// tsuru apps.
type Provisioner interface {
	Named

	// Provision is called when tsuru is creating the app.
	Provision(App) error

	// Destroy is called when tsuru is destroying the app.
	Destroy(App) error

	// AddUnits adds units to an app. The first parameter is the app, the
	// second is the number of units to be added.
	//
	// It returns a slice containing all added units
	AddUnits(App, uint, string, io.Writer) error

	// RemoveUnits "undoes" AddUnits, removing the given number of units
	// from the app.
	RemoveUnits(App, uint, string, io.Writer) error

	// Restart restarts the units of the application, with an optional
	// string parameter represeting the name of the process to start. When
	// the process is empty, Restart will restart all units of the
	// application.
	Restart(App, string, io.Writer) error

	// Start starts the units of the application, with an optional string
	// parameter representing the name of the process to start. When the
	// process is empty, Start will start all units of the application.
	Start(App, string) error

	// Stop stops the units of the application, with an optional string
	// parameter representing the name of the process to start. When the
	// process is empty, Stop will stop all units of the application.
	Stop(App, string) error

	// Units returns information about units by App.
	Units(App) ([]Unit, error)

	// RoutableAddresses returns the addresses used to access an application.
	RoutableAddresses(App) ([]url.URL, error)

	// Register a unit after the container has been created or restarted.
	RegisterUnit(App, string, map[string]interface{}) error
}

// ShellProvisioner is a provisioner that allows opening a shell to existing
// units.
type ShellProvisioner interface {
	// Open a remote shel in one of the units in the application.
	Shell(ShellOptions) error
}

// ExecutableProvisioner is a provisioner that allows executing commands on
// units.
type ExecutableProvisioner interface {
	// ExecuteCommand runs a command in all units of the app.
	ExecuteCommand(stdout, stderr io.Writer, app App, cmd string, args ...string) error

	// ExecuteCommandOnce runs a command in one unit of the app.
	ExecuteCommandOnce(stdout, stderr io.Writer, app App, cmd string, args ...string) error

	// ExecuteCommandIsolated runs a command in an new and ephemeral container.
	ExecuteCommandIsolated(stdout, stderr io.Writer, app App, cmd string, args ...string) error
}

// SleepableProvisioner is a provisioner that allows putting applications to
// sleep.
type SleepableProvisioner interface {
	// Sleep puts the units of the application to sleep, with an optional string
	// parameter representing the name of the process to sleep. When the
	// process is empty, Sleep will put all units of the application to sleep.
	Sleep(App, string) error
}

// MessageProvisioner is a provisioner that provides a welcome message for
// logging.
type MessageProvisioner interface {
	StartupMessage() (string, error)
}

// InitializableProvisioner is a provisioner that provides an initialization
// method that should be called when the app is started
type InitializableProvisioner interface {
	Initialize() error
}

// OptionalLogsProvisioner is a provisioner that allows optionally disabling
// logs for a given app.
type OptionalLogsProvisioner interface {
	// Checks if logs are enabled for given app.
	LogsEnabled(App) (bool, string, error)
}

// UnitStatusProvisioner is a provisioner that receive notifications about unit
// status changes.
type UnitStatusProvisioner interface {
	// SetUnitStatus changes the status of a unit.
	SetUnitStatus(Unit, Status) error
}

type AddNodeOptions struct {
	Address    string
	Metadata   map[string]string
	Register   bool
	CaCert     []byte
	ClientCert []byte
	ClientKey  []byte
	WaitTO     time.Duration
}

type RemoveNodeOptions struct {
	Address   string
	Rebalance bool
	Writer    io.Writer
}

type UpdateNodeOptions struct {
	Address  string
	Metadata map[string]string
	Enable   bool
	Disable  bool
}

type NodeProvisioner interface {
	// ListNodes returns a list of all nodes registered in the provisioner.
	ListNodes(addressFilter []string) ([]Node, error)

	// GetNode retrieves an existing node by its address.
	GetNode(address string) (Node, error)

	// AddNode adds a new node in the provisioner.
	AddNode(AddNodeOptions) error

	// RemoveNode removes an existing node.
	RemoveNode(RemoveNodeOptions) error

	// UpdateNode can be used to enable/disable a node and update its metadata.
	UpdateNode(UpdateNodeOptions) error

	// NodeForNodeData finds a node matching the received NodeStatusData.
	NodeForNodeData(NodeStatusData) (Node, error)
}

type RebalanceNodesOptions struct {
	Writer         io.Writer
	MetadataFilter map[string]string
	AppFilter      []string
	Dry            bool
	Force          bool
}

type NodeRebalanceProvisioner interface {
	RebalanceNodes(RebalanceNodesOptions) (bool, error)
}

type NodeContainerProvisioner interface {
	UpgradeNodeContainer(name string, pool string, writer io.Writer) error
	RemoveNodeContainer(name string, pool string, writer io.Writer) error
}

// UnitFinderProvisioner is a provisioner that allows finding a specific unit
// by its id. New provisioners should not implement this interface, this was
// only used during events format migration and is exclusive to docker
// provisioner.
type UnitFinderProvisioner interface {
	// GetAppFromUnitID returns an app from unit id
	GetAppFromUnitID(string) (App, error)
}

// AppFilterProvisioner is a provisioner that allows filtering apps by the
// state of its units.
type AppFilterProvisioner interface {
	FilterAppsByUnitStatus([]App, []string) ([]App, error)
}

type Node interface {
	Pool() string
	Address() string
	Status() string
	Metadata() map[string]string
	Units() ([]Unit, error)
	Provisioner() NodeProvisioner
}

type NodeExtraData interface {
	ExtraData() map[string]string
}

type NodeHealthChecker interface {
	Node
	FailureCount() int
	HasSuccess() bool
	ResetFailures()
}

type NodeSpec struct {
	// BSON tag for bson serialized compatibility with cluster.Node
	Address  string `bson:"_id"`
	Metadata map[string]string
	Status   string
	Pool     string
}

func NodeToSpec(n Node) NodeSpec {
	metadata := map[string]string{}
	if extra, ok := n.(NodeExtraData); ok {
		for k, v := range extra.ExtraData() {
			metadata[k] = v
		}
	}
	for k, v := range n.Metadata() {
		metadata[k] = v
	}
	return NodeSpec{
		Address:  n.Address(),
		Metadata: metadata,
		Status:   n.Status(),
		Pool:     n.Pool(),
	}
}

func NodeToJSON(n Node) ([]byte, error) {
	return json.Marshal(NodeToSpec(n))
}

type NodeStatusData struct {
	Addrs  []string
	Units  []UnitStatusData
	Checks []NodeCheckResult
}

type UnitStatusData struct {
	ID     string
	Name   string
	Status Status
}

type NodeCheckResult struct {
	Name       string
	Err        string
	Successful bool
}

// PlatformOptions is the set of options provided to PlatformAdd and
// PlatformUpdate, in the ExtensibleProvisioner.
type PlatformOptions struct {
	Name   string
	Args   map[string]string
	Input  io.Reader
	Output io.Writer
}

// ExtensibleProvisioner is a provisioner where administrators can manage
// platforms (automatically adding, removing and updating platforms).
type ExtensibleProvisioner interface {
	PlatformAdd(PlatformOptions) error
	PlatformUpdate(PlatformOptions) error
	PlatformRemove(name string) error
}

type provisionerFactory func() (Provisioner, error)

var provisioners = make(map[string]provisionerFactory)

// Register registers a new provisioner in the Provisioner registry.
func Register(name string, pFunc provisionerFactory) {
	provisioners[name] = pFunc
}

// Unregister unregisters a provisioner.
func Unregister(name string) {
	delete(provisioners, name)
}

// Get gets the named provisioner from the registry.
func Get(name string) (Provisioner, error) {
	pFunc, ok := provisioners[name]
	if !ok {
		return nil, errors.Errorf("unknown provisioner: %q", name)
	}
	return pFunc()
}

func GetDefault() (Provisioner, error) {
	if DefaultProvisioner == "" {
		DefaultProvisioner = defaultDockerProvisioner
	}
	return Get(DefaultProvisioner)
}

// Registry returns the list of registered provisioners.
func Registry() ([]Provisioner, error) {
	registry := make([]Provisioner, 0, len(provisioners))
	for _, pFunc := range provisioners {
		p, err := pFunc()
		if err != nil {
			return nil, err
		}
		registry = append(registry, p)
	}
	return registry, nil
}

func InitializeAll() error {
	provisioners, err := Registry()
	if err != nil {
		return err
	}
	var startupMessage string
	for _, p := range provisioners {
		if initializableProvisioner, ok := p.(InitializableProvisioner); ok {
			err = initializableProvisioner.Initialize()
			if err != nil {
				return err
			}
		}
		if messageProvisioner, ok := p.(MessageProvisioner); ok {
			startupMessage, err = messageProvisioner.StartupMessage()
			if err == nil && startupMessage != "" {
				fmt.Print(startupMessage)
			}
		}
	}
	return nil
}

// Error represents a provisioning error. It encapsulates further errors.
type Error struct {
	Reason string
	Err    error
}

// Error is the string representation of a provisioning error.
func (e *Error) Error() string {
	var err string
	if e.Err != nil {
		err = e.Err.Error() + ": " + e.Reason
	} else {
		err = e.Reason
	}
	return err
}

type TsuruYamlRestartHooks struct {
	Before []string
	After  []string
}

type TsuruYamlHooks struct {
	Restart TsuruYamlRestartHooks
	Build   []string
}

type TsuruYamlHealthcheck struct {
	Path            string
	Method          string
	Status          int
	Match           string
	RouterBody      string
	UseInRouter     bool `json:"use_in_router" bson:"use_in_router"`
	AllowedFailures int  `json:"allowed_failures" bson:"allowed_failures"`
}

func (hc TsuruYamlHealthcheck) ToRouterHC() router.HealthcheckData {
	if hc.UseInRouter {
		return router.HealthcheckData{
			Path:   hc.Path,
			Status: hc.Status,
			Body:   hc.RouterBody,
		}
	}
	return router.HealthcheckData{
		Path: "/",
	}
}

type TsuruYamlData struct {
	Hooks       TsuruYamlHooks
	Healthcheck TsuruYamlHealthcheck
}
