package embeddedpostgres

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mholt/archiver"
)

// EmbeddedPostgres maintains all configuration and runtime functions for maintaining the lifecycle of one Postgres process.
type EmbeddedPostgres struct {
	config              Config
	cacheLocator        CacheLocator
	remoteFetchStrategy RemoteFetchStrategy
	initDatabase        initDatabase
	createDatabase      createDatabase
	started             bool
}

// NewDatabase creates a new EmbeddedPostgres struct that can be used to start and stop a Postgres process.
// When called with no parameters it will assume a default configuration state provided by the DefaultConfig method.
// When called with parameters the first Config parameter will be used for configuration.
func NewDatabase(config ...Config) *EmbeddedPostgres {
	if len(config) < 1 {
		return newDatabaseWithConfig(DefaultConfig())
	}

	return newDatabaseWithConfig(config[0])
}

func newDatabaseWithConfig(config Config) *EmbeddedPostgres {
	versionStrategy := defaultVersionStrategy(config)
	cacheLocator := defaultCacheLocator(versionStrategy)
	remoteFetchStrategy := defaultRemoteFetchStrategy("https://repo1.maven.org", versionStrategy, cacheLocator)

	return &EmbeddedPostgres{
		config:              config,
		cacheLocator:        cacheLocator,
		remoteFetchStrategy: remoteFetchStrategy,
		initDatabase:        defaultInitDatabase,
		createDatabase:      defaultCreateDatabase,
		started:             false,
	}
}

// Install will make filesystem modifications, retrieving and extracting the PostgreSQL binaries into the configured directory.
func (ep *EmbeddedPostgres) Install() error {
	cacheLocation, exists := ep.cacheLocator()
	if !exists {
		if err := ep.remoteFetchStrategy(); err != nil {
			return err
		}
	}

	binaryExtractLocation := userLocationOrDefault(ep.config.runtimePath, cacheLocation)
	if err := os.RemoveAll(binaryExtractLocation); err != nil {
		return fmt.Errorf("unable to clean up directory %s with error: %s", binaryExtractLocation, err)
	}

	if err := archiver.NewTarXz().Unarchive(cacheLocation, binaryExtractLocation); err != nil {
		return fmt.Errorf("unable to extract postgres archive %s to %s", cacheLocation, binaryExtractLocation)
	}

	if err := ep.initDatabase(binaryExtractLocation, ep.config.username, ep.config.password, ep.config.locale); err != nil {
		return err
	}

	return nil
}

// CreateDatabase will issue the "CREATE DATABASE" command on a running server
func (ep *EmbeddedPostgres) CreateDatabase() error {
	if !ep.started {
		return errors.New("server is not started")
	}

	cacheLocation, _ := ep.cacheLocator()
	binaryExtractLocation := userLocationOrDefault(ep.config.runtimePath, cacheLocation)
	if err := ep.createDatabase(ep.config.port, ep.config.username, ep.config.password, ep.config.database); err != nil {
		if stopErr := stopPostgres(binaryExtractLocation); stopErr != nil {
			return fmt.Errorf("unable to stop database casused by error %s", err)
		}

		return err
	}

	return nil
}

func (ep *EmbeddedPostgres) IsStarted() bool {
    return ep.started
}

// Start will try to start the configured Postgres process returning an error when there were any problems with invocation.
// If any error occurs Start will try to also Stop the Postgres process in order to not leave any sub-process running.
func (ep *EmbeddedPostgres) Start() error {
	if ep.started {
		return errors.New("server is already started")
	}

	if err := ensurePortAvailable(ep.config.port); err != nil {
		return err
	}

	cacheLocation, _ := ep.cacheLocator()
	binaryExtractLocation := userLocationOrDefault(ep.config.runtimePath, cacheLocation)
	if err := startPostgres(binaryExtractLocation, ep.config); err != nil {
		return err
	}

	ep.started = true

/*
    commenting this out because I think it's screwing things up because the database has not yet been created.
	if err := healthCheckDatabaseOrTimeout(ep.config); err != nil {
		if stopErr := stopPostgres(binaryExtractLocation); stopErr != nil {
			return fmt.Errorf("unable to stop database casused by error %s", err)
		}

		return err
	}
*/

	return nil
}

// Stop will try to stop the Postgres process gracefully returning an error when there were any problems.
func (ep *EmbeddedPostgres) Stop() error {
	cacheLocation, exists := ep.cacheLocator()
	if !exists || !ep.started {
		return errors.New("server has not been started")
	}

	binaryExtractLocation := userLocationOrDefault(ep.config.runtimePath, cacheLocation)
	if err := stopPostgres(binaryExtractLocation); err != nil {
		return err
	}

	ep.started = false

	return nil
}

func startPostgres(binaryExtractLocation string, config Config) error {
	postgresBinary := filepath.Join(binaryExtractLocation, "bin/pg_ctl")
	postgresProcess := exec.Command(postgresBinary, "start", "-w",
		"-D", filepath.Join(binaryExtractLocation, "data"),
		"-o", fmt.Sprintf(`"-p %d"`, config.port))
	log.Println(postgresProcess.String())
	postgresProcess.Stderr = os.Stderr
	postgresProcess.Stdout = os.Stdout

	if err := postgresProcess.Run(); err != nil {
		return fmt.Errorf("could not start postgres using %s", postgresProcess.String())
	}

	return nil
}

func stopPostgres(binaryExtractLocation string) error {
	postgresBinary := filepath.Join(binaryExtractLocation, "bin/pg_ctl")
	postgresProcess := exec.Command(postgresBinary, "stop", "-w",
		"-D", filepath.Join(binaryExtractLocation, "data"))
	postgresProcess.Stderr = os.Stderr
	postgresProcess.Stdout = os.Stdout

	return postgresProcess.Run()
}

func ensurePortAvailable(port uint32) error {
	conn, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return fmt.Errorf("process already listening on port %d", port)
	}

	if err := conn.Close(); err != nil {
		return err
	}

	return nil
}

func userLocationOrDefault(userLocation, cacheLocation string) string {
	if userLocation != "" {
		return userLocation
	}

	return filepath.Join(filepath.Dir(cacheLocation), "extracted")
}
