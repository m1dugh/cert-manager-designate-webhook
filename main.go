package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/rest"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/config"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
)

var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	// This will register our custom DNS provider with the webhook serving
	// library, making it available as an API under the provided GroupName.
	// You can register multiple DNS provider implementations with a single
	// webhook, where the Name() method will be used to disambiguate between
	// the different implementations.
	cmd.RunWebhookServer(GroupName,
		&designateSolver{},
	)
}

// designateSolver implements the provider-specific logic needed to
// 'present' an ACME challenge TXT record for your own DNS provider.
// To do so, it must implement the `github.com/cert-manager/cert-manager/pkg/acme/webhook.Solver`
// interface.
type designateSolver struct {
	// If a Kubernetes 'clientset' is needed, you must:
	// 1. uncomment the additional `client` field in this structure below
	// 2. uncomment the "k8s.io/client-go/kubernetes" import at the top of the file
	// 3. uncomment the relevant code in the Initialize method below
	// 4. ensure your webhook's service account has the required RBAC role
	//    assigned to it for interacting with the Kubernetes APIs you need.
	//client kubernetes.Clientset
	dnsClient *gophercloud.ServiceClient
}

// designateConfig is a structure that is used to decode into when
// solving a DNS01 challenge.
// This information is provided by cert-manager, and may be a reference to
// additional configuration that's needed to solve the challenge for this
// particular certificate or issuer.
// This typically includes references to Secret resources containing DNS
// provider credentials, in cases where a 'multi-tenant' DNS solver is being
// created.
// If you do *not* require per-issuer or per-certificate configuration to be
// provided to your webhook, you can skip decoding altogether in favour of
// using CLI flags or similar to provide configuration.
// You should not include sensitive information here. If credentials need to
// be used by your provider here, you should reference a Kubernetes Secret
// resource and fetch these credentials using a Kubernetes clientset.
type designateConfig struct {
	// Change the two fields below according to the format of the configuration
	// to be decoded.
	// These fields will be set by users in the
	// `issuer.spec.acme.dns01.providers.webhook.config` field.

	//Email           string `json:"email"`
	//APIKeySecretRef v1alpha1.SecretKeySelector `json:"apiKeySecretRef"`
	ZoneID string `json:"zone_id"`
}

// Name is used as the name for this DNS solver when referencing it on the ACME
// Issuer resource.
// This should be unique **within the group name**, i.e. you can have two
// solvers configured with the same Name() **so long as they do not co-exist
// within a single webhook deployment**.
// For example, `cloudflare` may be used as the name of a solver.
func (c *designateSolver) Name() string {
	return "designate-solver"
}

func (c *designateSolver) recordExists(name string, cfg *designateConfig) (*recordsets.RecordSet, error) {

	listOptions := recordsets.ListOpts{
		Type: "TXT",
		Name: name,
	}

	pages, err := recordsets.ListByZone(c.dnsClient, cfg.ZoneID, listOptions).AllPages(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("Could not list records by zone : %s", err)
	}

	allRecords, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return nil, fmt.Errorf("Error extracting pages : %s", err)
	}

	if len(allRecords) > 0 {
		return &allRecords[0], nil
	} else {
		return nil, nil
	}
}

// Present is responsible for actually presenting the DNS record with the
// DNS provider.
// This method should tolerate being called multiple times with the same value.
// cert-manager itself will later perform a self check to ensure that the
// solver has correctly configured the DNS provider.
func (c *designateSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}

	// TODO: do something more useful with the decoded configuration
	fmt.Printf("Decoded configuration %v", cfg)

	authOptions, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return fmt.Errorf("Could not load config : %s", err)
	}
	fmt.Printf("Loaded auth options\n")

	client, err := config.NewProviderClient(context.Background(), authOptions)
	if err != nil {
		return fmt.Errorf("Openstack provider config err : %s", err)
	}

	c.dnsClient, err = openstack.NewDNSV2(client, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return fmt.Errorf("Error instantiating dnsv2 client : %s", err)
	}

	rr, err := c.recordExists(ch.ResolvedFQDN, &cfg)
	if err != nil {
		return fmt.Errorf("Could not check if record %s exists : %s", ch.ResolvedFQDN, err)
	}

	if rr != nil {
		if len(rr.Records) == 1 && rr.Records[0] == ch.Key {
			return nil
		}

		updateOpts := recordsets.UpdateOpts{
			Records: []string{ch.Key},
		}

		err = recordsets.Update(context.TODO(), c.dnsClient, cfg.ZoneID, rr.ID, updateOpts).Err
		if err != nil {
			return fmt.Errorf("Could not update record : %s", err)
		}

	} else {
		// create record
		createOpts := recordsets.CreateOpts{
			Name:        ch.ResolvedFQDN,
			Type:        "TXT",
			TTL:         600,
			Description: fmt.Sprintf("The acme record for %s", ch.DNSName),
			Records:     []string{ch.Key},
		}

		err = recordsets.Create(context.TODO(), c.dnsClient, cfg.ZoneID, createOpts).Err
		if err != nil {
			return fmt.Errorf("Could not create record : %s", err)
		}
	}

	return nil
}

// CleanUp should delete the relevant TXT record from the DNS provider console.
// If multiple TXT records exist with the same record name (e.g.
// _acme-challenge.example.com) then **only** the record with the same `key`
// value provided on the ChallengeRequest should be cleaned up.
// This is in order to facilitate multiple DNS validations for the same domain
// concurrently.
func (c *designateSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	// TODO: add code that deletes a record from the DNS provider's console
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}

	listOptions := recordsets.ListOpts{
		Type: "TXT",
		Name: ch.ResolvedFQDN,
	}

	pages, err := recordsets.ListByZone(c.dnsClient, cfg.ZoneID, listOptions).AllPages(context.TODO())
	if err != nil {
		return fmt.Errorf("Could not list records by zone : %s", err)
	}

	allRecords, err := recordsets.ExtractRecordSets(pages)
	if err != nil {
		return fmt.Errorf("Error extracting pages : %s", err)
	}

	for _, rec := range allRecords {
		if err = recordsets.Delete(context.Background(), c.dnsClient, rec.ZoneID, rec.ID).ExtractErr(); err != nil {
			return fmt.Errorf("Could not delete record %s in zone %s : %s", rec.ID, rec.ZoneID, err)
		}
	}

	return nil
}

// Initialize will be called when the webhook first starts.
// This method can be used to instantiate the webhook, i.e. initialising
// connections or warming up caches.
// Typically, the kubeClientConfig parameter is used to build a Kubernetes
// client that can be used to fetch resources from the Kubernetes API, e.g.
// Secret resources containing credentials used to authenticate with DNS
// provider accounts.
// The stopCh can be used to handle early termination of the webhook, in cases
// where a SIGTERM or similar signal is sent to the webhook process.
func (c *designateSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	///// UNCOMMENT THE BELOW CODE TO MAKE A KUBERNETES CLIENTSET AVAILABLE TO
	///// YOUR CUSTOM DNS PROVIDER

	//cl, err := kubernetes.NewForConfig(kubeClientConfig)
	//if err != nil {
	//	return err
	//}
	//
	//c.client = cl

	///// END OF CODE TO MAKE KUBERNETES CLIENTSET AVAILABLE
	return nil
}

// loadConfig is a small helper function that decodes JSON configuration into
// the typed config struct.
func loadConfig(cfgJSON *extapi.JSON) (designateConfig, error) {
	cfg := designateConfig{}
	// handle the 'base case' where no configuration has been provided
	if cfgJSON == nil {
		return designateConfig{}, fmt.Errorf("Missing zone_id field")
	}

	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	return cfg, nil
}
