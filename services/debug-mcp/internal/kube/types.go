package kube

type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

type NodeList struct {
	Items []Node `json:"items"`
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

type NodeSpec struct {
	Taints []Taint `json:"taints,omitempty"`
}

type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect,omitempty"`
}

type NodeStatus struct {
	Conditions  []Condition       `json:"conditions,omitempty"`
	Allocatable map[string]string `json:"allocatable,omitempty"`
	NodeInfo    NodeSystemInfo    `json:"nodeInfo"`
}

type NodeSystemInfo struct {
	KubeletVersion string `json:"kubeletVersion,omitempty"`
}

type NamespaceList struct {
	Items []Namespace `json:"items"`
}

type Namespace struct {
	Metadata ObjectMeta      `json:"metadata"`
	Status   NamespaceStatus `json:"status"`
}

type NamespaceStatus struct {
	Phase string `json:"phase,omitempty"`
}

type PodList struct {
	Items []Pod `json:"items"`
}

type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

type PodSpec struct {
	NodeName       string      `json:"nodeName,omitempty"`
	RestartPolicy  string      `json:"restartPolicy,omitempty"`
	ServiceAccount string      `json:"serviceAccountName,omitempty"`
	InitContainers []Container `json:"initContainers,omitempty"`
	Containers     []Container `json:"containers,omitempty"`
}

type Container struct {
	Name      string               `json:"name"`
	Image     string               `json:"image"`
	Env       []EnvVar             `json:"env,omitempty"`
	EnvFrom   []EnvFromSource      `json:"envFrom,omitempty"`
	Resources ResourceRequirements `json:"resources,omitempty"`
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	ConfigMapKeyRef  *ObjectKeySelector     `json:"configMapKeyRef,omitempty"`
	SecretKeyRef     *ObjectKeySelector     `json:"secretKeyRef,omitempty"`
	FieldRef         *FieldSelector         `json:"fieldRef,omitempty"`
	ResourceFieldRef *ResourceFieldSelector `json:"resourceFieldRef,omitempty"`
}

type ObjectKeySelector struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

type FieldSelector struct {
	FieldPath string `json:"fieldPath,omitempty"`
}

type ResourceFieldSelector struct {
	Resource string `json:"resource,omitempty"`
}

type EnvFromSource struct {
	Prefix       string          `json:"prefix,omitempty"`
	ConfigMapRef *NamedObjectRef `json:"configMapRef,omitempty"`
	SecretRef    *NamedObjectRef `json:"secretRef,omitempty"`
}

type NamedObjectRef struct {
	Name string `json:"name,omitempty"`
}

type ResourceRequirements struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type PodStatus struct {
	Phase                 string            `json:"phase,omitempty"`
	PodIP                 string            `json:"podIP,omitempty"`
	HostIP                string            `json:"hostIP,omitempty"`
	Reason                string            `json:"reason,omitempty"`
	Message               string            `json:"message,omitempty"`
	QOSClass              string            `json:"qosClass,omitempty"`
	Conditions            []Condition       `json:"conditions,omitempty"`
	InitContainerStatuses []ContainerStatus `json:"initContainerStatuses,omitempty"`
	ContainerStatuses     []ContainerStatus `json:"containerStatuses,omitempty"`
}

type ContainerStatus struct {
	Name         string          `json:"name"`
	Ready        bool            `json:"ready"`
	RestartCount int             `json:"restartCount"`
	Image        string          `json:"image,omitempty"`
	State        *ContainerState `json:"state,omitempty"`
	LastState    *ContainerState `json:"lastState,omitempty"`
}

type ContainerState struct {
	Running    *ContainerStateRunning    `json:"running,omitempty"`
	Waiting    *ContainerStateWaiting    `json:"waiting,omitempty"`
	Terminated *ContainerStateTerminated `json:"terminated,omitempty"`
}

type ContainerStateRunning struct {
	StartedAt string `json:"startedAt,omitempty"`
}

type ContainerStateWaiting struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type ContainerStateTerminated struct {
	ExitCode   int    `json:"exitCode"`
	Signal     int    `json:"signal,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

type DeploymentList struct {
	Items []Deployment `json:"items"`
}

type Deployment struct {
	Metadata ObjectMeta       `json:"metadata"`
	Spec     WorkloadSpec     `json:"spec"`
	Status   DeploymentStatus `json:"status"`
}

type WorkloadSpec struct {
	Replicas *int        `json:"replicas,omitempty"`
	Template PodTemplate `json:"template,omitempty"`
}

type PodTemplate struct {
	Spec PodSpec `json:"spec,omitempty"`
}

type DeploymentStatus struct {
	Replicas            int `json:"replicas,omitempty"`
	ReadyReplicas       int `json:"readyReplicas,omitempty"`
	UpdatedReplicas     int `json:"updatedReplicas,omitempty"`
	AvailableReplicas   int `json:"availableReplicas,omitempty"`
	UnavailableReplicas int `json:"unavailableReplicas,omitempty"`
}

type StatefulSetList struct {
	Items []StatefulSet `json:"items"`
}

type StatefulSet struct {
	Metadata ObjectMeta        `json:"metadata"`
	Spec     WorkloadSpec      `json:"spec"`
	Status   StatefulSetStatus `json:"status"`
}

type StatefulSetStatus struct {
	Replicas        int `json:"replicas,omitempty"`
	ReadyReplicas   int `json:"readyReplicas,omitempty"`
	UpdatedReplicas int `json:"updatedReplicas,omitempty"`
}

type DaemonSetList struct {
	Items []DaemonSet `json:"items"`
}

type DaemonSet struct {
	Metadata ObjectMeta      `json:"metadata"`
	Spec     WorkloadSpec    `json:"spec"`
	Status   DaemonSetStatus `json:"status"`
}

type DaemonSetStatus struct {
	DesiredNumberScheduled int `json:"desiredNumberScheduled,omitempty"`
	NumberReady            int `json:"numberReady,omitempty"`
	UpdatedNumberScheduled int `json:"updatedNumberScheduled,omitempty"`
	NumberUnavailable      int `json:"numberUnavailable,omitempty"`
}

type JobList struct {
	Items []Job `json:"items"`
}

type Job struct {
	Metadata ObjectMeta `json:"metadata"`
	Status   JobStatus  `json:"status"`
}

type JobStatus struct {
	Active    int `json:"active,omitempty"`
	Succeeded int `json:"succeeded,omitempty"`
	Failed    int `json:"failed,omitempty"`
}

type CronJobList struct {
	Items []CronJob `json:"items"`
}

type CronJob struct {
	Metadata ObjectMeta    `json:"metadata"`
	Spec     CronJobSpec   `json:"spec"`
	Status   CronJobStatus `json:"status"`
}

type CronJobSpec struct {
	Schedule string `json:"schedule,omitempty"`
	Suspend  bool   `json:"suspend,omitempty"`
}

type CronJobStatus struct {
	LastScheduleTime string `json:"lastScheduleTime,omitempty"`
}

type EventList struct {
	Items []Event `json:"items"`
}

type Event struct {
	Metadata       ObjectMeta     `json:"metadata"`
	Type           string         `json:"type,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Message        string         `json:"message,omitempty"`
	Count          int            `json:"count,omitempty"`
	FirstTimestamp string         `json:"firstTimestamp,omitempty"`
	LastTimestamp  string         `json:"lastTimestamp,omitempty"`
	EventTime      string         `json:"eventTime,omitempty"`
	InvolvedObject InvolvedObject `json:"involvedObject"`
}

type InvolvedObject struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

type FluxResourceList struct {
	Items []FluxResource `json:"items"`
}

type FluxResource struct {
	Metadata ObjectMeta         `json:"metadata"`
	Spec     FluxResourceSpec   `json:"spec"`
	Status   FluxResourceStatus `json:"status"`
}

type FluxResourceSpec struct {
	Suspend bool `json:"suspend,omitempty"`
}

type FluxResourceStatus struct {
	Conditions            []Condition `json:"conditions,omitempty"`
	LastAppliedRevision   string      `json:"lastAppliedRevision,omitempty"`
	LastAttemptedRevision string      `json:"lastAttemptedRevision,omitempty"`
	Artifact              *struct {
		Revision string `json:"revision,omitempty"`
	} `json:"artifact,omitempty"`
}
