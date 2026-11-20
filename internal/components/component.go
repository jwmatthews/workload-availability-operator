package components

// WebhookConfiguration names an admission configuration rendered for the component.
type WebhookConfiguration struct {
	Kind string // ValidatingWebhookConfiguration or MutatingWebhookConfiguration
	Name string
}

// ResourceKind identifies a native API kind of a component.
type ResourceKind struct {
	Group, Version, Kind string
}

// Definition describes one operand component.
type Definition struct {
	Key         string // API key, e.g. nodeHealthCheck
	ShortName   string // nhc
	ManifestDir string // directory under internal/operands/manifests
	Deployments []string
	// ManagerDeployment and ManagerContainer select the target for manager resources.
	ManagerDeployment string
	ManagerContainer  string
	Images            []string // RELATED_IMAGE_* names; see images.Placeholder
	Webhooks          []WebhookConfiguration
	// AgentOwners are config kinds whose DaemonSets are node agents.
	// Their namespace scope follows AgentsInAllNamespaces.
	AgentOwners []ResourceKind
	// AgentsInAllNamespaces includes agents and their configs outside the install
	// namespace when the component manager supports them there.
	AgentsInAllNamespaces bool
	// ManagedKinds are native kinds the manager reconciles (and may hold finalizers on). Objects
	// of these kinds that exist anywhere in the cluster while the component is Disabled are
	// no longer reconciled and are reported as retained.
	ManagedKinds []ResourceKind
	// AgentDaemonSetSelector matches, by pre-existing operand labels, the runtime agent
	// DaemonSet(s) a component creates. Their namespace scope follows AgentsInAllNamespaces.
	// When the component is Disabled these DaemonSets are deleted. Empty for components with no node agent.
	AgentDaemonSetSelector map[string]string
}

// All returns the POC operand set in reconcile order.
func All() []Definition { return []Definition{NHC(), SNR(), SBR(), FAR(), MDR(), NMO()} }

// ByKey finds a definition by API key.
func ByKey(key string) (Definition, bool) {
	for _, d := range All() {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}
