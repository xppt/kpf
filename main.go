package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes/scheme"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util"
)

func main() {
	os.Exit(runKpf())
}

func runKpf() int {
	nsFlag := flag.String("n", "", "")
	flag.Parse()

	if len(flag.Args()) < 3 {
		log.Fatal("usage: kpf [-n <ns>] <pod-or-similar> <port> <prog>...")
	}

	resource := flag.Args()[0]
	remotePort := flag.Args()[1]
	progTemplate := flag.Args()[2:]

	cmdFactory := cmdutil.NewFactory(genericclioptions.NewConfigFlags(true))

	ns := *nsFlag
	if ns == "" {
		ns = getConfigNs(ns, cmdFactory)
	}

	obj := findObject(cmdFactory, ns, resource)

	pod, err := polymorphichelpers.AttachablePodForObjectFn(cmdFactory, obj, time.Minute)
	dieOnError(err, "unable to find pod")

	targetPort, err := convertObjPortToPodPort(remotePort, obj, pod)

	session, err := StartPortFw(context.Background(), &PortFwCfg{
		CmdFactory: cmdFactory,
		Ns: ns,
		PodName: pod.Name,
		Port: targetPort,
	})
	dieOnError(err, "unable to forward")
	defer session.Close()

	prog := expandProgTemplate(progTemplate, strconv.Itoa(session.BindedPort))

	execRes := execProg(prog)

	if !execRes.Exited() {
		log.Printf("prog: %s", execRes)
		return 1
	}

	return execRes.ExitCode()
}

func getConfigNs(ns string, cmdFactory cmdutil.Factory) string {
	ns, _, err := cmdFactory.ToRawKubeConfigLoader().Namespace()
	dieOnError(err, "unable to get current namespace")
	return ns
}

func findObject(cmdFactory cmdutil.Factory, ns string, name string) runtime.Object {
	builder := cmdFactory.NewBuilder().
		WithScheme(scheme.Scheme, scheme.Scheme.PrioritizedVersionsAllGroups()...).
		NamespaceParam(ns)

	obj, err := builder.ResourceNames("pod", name).Do().Object()
	dieOnError(err, "unable to find object")
	return obj
}

func convertObjPortToPodPort(port string, obj runtime.Object, pod *corev1.Pod) (int, error) {
	switch typedObj := obj.(type) {
	case *corev1.Service:
		return translateSvcPortToPodPort(port, *typedObj, *pod)
	default:
		return convertPodNamedPortToNumber(port, *pod)
	}
}

func translateSvcPortToPodPort(port string, svc corev1.Service, pod corev1.Pod) (int, error) {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		svcPort, err := util.LookupServicePortNumberByName(svc, port)
		if err != nil {
			return 0, err
		}
		portNum = int(svcPort)
	}

	containerPort, err := util.LookupContainerPortNumberByServicePort(svc, pod, int32(portNum))
	if err != nil {
		return 0, err
	}

	return int(containerPort), nil
}

func convertPodNamedPortToNumber(port string, pod corev1.Pod) (int, error) {
	portNum, err := strconv.Atoi(port)
	if err == nil {
		return portNum, nil
	}

	portNumByName, err := util.LookupContainerPortNumberByName(pod, port)
	if err != nil {
		return 0, err
	}

	return int(portNumByName), nil
}

func expandProgTemplate(prog []string, param string) []string {
	var result []string
	for index, progArg := range prog {
		if progArg == "_" {
			result = append(result, param)
			result = append(result, prog[index + 1:]...)
			return result
		} else {
			result = append(result, progArg)
		}
	}
	return result
}

func execProg(args []string) *os.ProcessState {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	dieOnError(err, "failed to start prog")

	// Ignore SIGINT, now we only want the child to be interrupted
	signal.Ignore(os.Interrupt)

	cmd.Wait()

	return cmd.ProcessState
}

func dieOnError(err error, msg string) {
	if err != nil {
		log.Printf("%s: %v\n", msg, err)
		os.Exit(1)
	}
}
