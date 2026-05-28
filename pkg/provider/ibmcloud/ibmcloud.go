package ibmcloud

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/redhat-developer/mapt/pkg/manager"
	mc "github.com/redhat-developer/mapt/pkg/manager/context"
	"github.com/redhat-developer/mapt/pkg/manager/credentials"
	"github.com/redhat-developer/mapt/pkg/provider/aws/services/s3"
	icConstants "github.com/redhat-developer/mapt/pkg/provider/ibmcloud/constants"
	"github.com/redhat-developer/mapt/pkg/util/logging"
)

const (
	LOCATION_ENV    = "IC_REGION"
	pulumiLocksPath = ".pulumi/locks"
)

type IBMCloud struct{}

func (i *IBMCloud) Init(ctx context.Context, backedURL string) error {
	if isS3Path(backedURL) {
		return manageCOSRemoteState(backedURL)
	}
	return nil
}

func (a *IBMCloud) DefaultHostingPlace() (*string, error) {
	hp := os.Getenv("IC_REGION")
	if len(hp) > 0 {
		return &hp, nil
	}
	return nil, fmt.Errorf("missing default value for IBM Cloud Region: IC_REGION")
}

func (a *IBMCloud) Zone() (*string, error) {
	hp := os.Getenv("IC_ZONE")
	if len(hp) > 0 {
		return &hp, nil
	}
	return nil, fmt.Errorf("missing default value for IBM Cloud Region: IC_ZONE")
}

func Provider() *IBMCloud {
	return &IBMCloud{}
}

func GetClouProviderCredentials(fixedCredentials map[string]string) credentials.ProviderCredentials {
	return credentials.ProviderCredentials{
		SetCredentialFunc: nil,
		FixedCredentials:  fixedCredentials}
}

var (
	DefaultCredentials = GetClouProviderCredentials(nil)
)

func isS3Path(backedURL string) bool {
	return strings.HasPrefix(backedURL, "s3://")
}

func ensureHTTPS(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	if strings.HasPrefix(endpoint, "http://") {
		return "https://" + strings.TrimPrefix(endpoint, "http://")
	}
	return "https://" + endpoint
}

func requireEnv(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required when using S3-compatible backend", name)
	}
	return v, nil
}

func manageCOSRemoteState(backedURL string) error {
	accountID, err := requireEnv(icConstants.EnvIBMCloudAccount)
	if err != nil {
		return err
	}
	apiKey, err := requireEnv(icConstants.EnvIBMCloudAPIKey)
	if err != nil {
		return err
	}
	region, err := requireEnv(LOCATION_ENV)
	if err != nil {
		return err
	}

	endpoint, _ := os.LookupEnv(icConstants.EnvIBMCosEndpoint)
	if endpoint == "" {
		endpoint = fmt.Sprintf("s3.%s.cloud-object-storage.appdomain.cloud", region)
	}

	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":     accountID,
		"AWS_SECRET_ACCESS_KEY": apiKey,
		"AWS_ENDPOINT_URL":      ensureHTTPS(endpoint),
		"AWS_REGION":            region,
		"AWS_DEFAULT_REGION":    region,
		"AWS_S3_USE_PATH_STYLE": "true",
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

func parseS3BackedURL(mCtx *mc.Context) (*string, *string, error) {
	if !strings.HasPrefix(mCtx.BackedURL(), "s3://") {
		return nil, nil, fmt.Errorf("invalid S3 URI: must start with s3://")
	}
	u, err := url.Parse(mCtx.BackedURL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse S3 URI: %w", err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	if key == "" {
		return nil, nil, fmt.Errorf("invalid S3 URI %q: missing object key after bucket name", mCtx.BackedURL())
	}
	return &u.Host, &key, nil
}

func DestroyStack(mCtx *mc.Context, stackName string) error {
	logging.Debug("Running destroy operation")
	if len(stackName) == 0 {
		return fmt.Errorf("stackname is required")
	}
	if mCtx.IsForceDestroy() {
		bucket, key, err := parseS3BackedURL(mCtx)
		if err != nil {
			logging.Error(err)
		} else {
			lockPathKey := fmt.Sprintf("%s/%s", *key, pulumiLocksPath)
			if err := s3.Delete(mCtx.Context(), bucket, &lockPathKey); err != nil {
				logging.Error(err)
			}
		}
	}
	stack := manager.Stack{
		StackName:           mCtx.StackNameByProject(stackName),
		ProjectName:         mCtx.ProjectName(),
		BackedURL:           mCtx.BackedURL(),
		ProviderCredentials: DefaultCredentials,
	}
	return manager.DestroyStack(mCtx, stack)
}

func CleanupState(mCtx *mc.Context) error {
	if mCtx.IsKeepState() {
		return nil
	}

	bucket, key, parseErr := parseS3BackedURL(mCtx)
	if parseErr != nil {
		logging.Warnf("Failed to parse S3 backend URL, skipping state cleanup: %v", parseErr)
		return nil
	}

	logging.Infof("Cleaning up Pulumi state from s3://%s/%s", *bucket, *key)
	if deleteErr := s3.Delete(mCtx.Context(), bucket, key); deleteErr != nil {
		logging.Warnf("Failed to cleanup S3 state: %v", deleteErr)
	} else {
		logging.Info("Successfully cleaned up Pulumi state from S3")
	}

	return nil
}

func Destroy(mCtx *mc.Context, stackName string) error {
	stack := manager.Stack{
		StackName:           mCtx.StackNameByProject(stackName),
		ProjectName:         mCtx.ProjectName(),
		BackedURL:           mCtx.BackedURL(),
		ProviderCredentials: DefaultCredentials}
	return manager.DestroyStack(mCtx, stack)
}

type gen2Location struct {
	region, zone string
}

type classicLocation string

var LocationMapping = map[classicLocation]gen2Location{
	"dal10": {region: "us-south", zone: "us-south-1"},
	"dal12": {region: "us-south", zone: "us-south-2"},
	"dal13": {region: "us-south", zone: "us-south-3"},
	"wdc06": {region: "us-east", zone: "us-east-1"},
	"tor01": {region: "ca-tor", zone: "ca-tor-1"},
	"mon01": {region: "ca-mon", zone: "us-south-2"},
	"lon04": {region: "eu-gb", zone: "eu-gb-1"},
	"fra04": {region: "eu-de", zone: "eu-de-1"},
	"fra05": {region: "eu-de", zone: "eu-de-2"},
	"syd04": {region: "au-syd", zone: "au-syd-1"},
	"tok04": {region: "jp-tok", zone: "jp-tok-1"}}

func ClassicLocation() *classicLocation {
	for k, v := range LocationMapping {
		if v.region == os.Getenv("IC_REGION") && v.zone == os.Getenv("IC_ZONE") {
			return &k
		}
	}
	return nil
}
