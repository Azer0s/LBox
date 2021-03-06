package main

import (
	"fmt"
	"github.com/Azer0s/Hummus/interpreter"
	"github.com/Azer0s/Hummus/lexer"
	"github.com/Azer0s/Hummus/parser"
	"sync"
	"time"
)

func main() {}

// CALL concurrency functions
var CALL string = "--system-do-sync!"

var channelMap = make(map[int]chan interpreter.Node, 0)
var channelParent = make(map[int]int, 0)
var watcheeToWatcher = make(map[int]map[int]int, 0)
var watcherToWatchee = make(map[int]map[int]int, 0)
var currentPid = 0

var currentPidMu = &sync.RWMutex{}
var channelMapMu = &sync.RWMutex{}
var channelParentMu = &sync.RWMutex{}
var watcheeToWatcherMu = &sync.RWMutex{}
var watcherToWatcheeMu = &sync.RWMutex{}

var initOnce sync.Once

// MAILBOX_BUFFER buffer for channel mailboxes
const MAILBOX_BUFFER = 1024

func createPidChannel(self int) (pid int) {
	channel := make(chan interpreter.Node, MAILBOX_BUFFER)

	currentPidMu.Lock()

	channelMapMu.RLock()
	for {
		if _, ok := channelMap[currentPid]; !ok {
			channelMapMu.RUnlock()
			break
		}

		currentPid++
	}

	pid = currentPid

	channelParentMu.Lock()
	channelParent[pid] = self
	channelParentMu.Unlock()

	channelMapMu.Lock()
	channelMap[pid] = channel
	channelMapMu.Unlock()

	currentPid++

	currentPidMu.Unlock()

	return
}

func createWatch(watcher, watchee int) {
	channelMapMu.RLock()
	if _, ok := channelMap[watchee]; !ok {
		panic(fmt.Sprintf("Process %d does not exist!", watchee))
	}
	channelMapMu.RUnlock()

	watcheeToWatcherMu.Lock()
	if _, ok := watcheeToWatcher[watchee]; !ok {
		watcheeToWatcher[watchee] = make(map[int]int, 0)
	}
	watcheeToWatcherMu.Unlock()

	watcherToWatcheeMu.Lock()
	if _, ok := watcherToWatchee[watcher]; !ok {
		watcherToWatchee[watcher] = make(map[int]int, 0)
	}
	watcherToWatcheeMu.Unlock()

	watcheeToWatcherMu.Lock()
	watcheeToWatcher[watchee][watcher] = 1
	watcheeToWatcherMu.Unlock()

	watcherToWatcheeMu.Lock()
	watcherToWatchee[watcher][watchee] = 1
	watcherToWatcheeMu.Unlock()

}

func doWatch(arg interpreter.Node, variables *map[string]interpreter.Node) interpreter.Node {
	interpreter.EnsureSingleType(&arg, 1, interpreter.NODETYPE_INT, CALL+" :watch")
	createWatch((*variables)[interpreter.SELF].Value.(int), arg.Value.(int))
	return interpreter.Nothing
}

func doSend(pid, val interpreter.Node) interpreter.Node {
	interpreter.EnsureSingleType(&pid, 1, interpreter.NODETYPE_INT, CALL+" :send")

	channelMapMu.RLock()
	channel := channelMap[pid.Value.(int)]
	channelMapMu.RUnlock()

	select {
	case channel <- val:
	default:
		//No data sent
	}

	return interpreter.Nothing
}

func doReceive(variables *map[string]interpreter.Node) interpreter.Node {
	self := (*variables)[interpreter.SELF].Value.(int)

	channelMapMu.RLock()
	channel := channelMap[self]
	channelMapMu.RUnlock()

	val := <-channel
	return val
}

func doReceiveWithTimeout(variables *map[string]interpreter.Node, timeout interpreter.Node) interpreter.Node {
	self := (*variables)[interpreter.SELF].Value.(int)

	interpreter.EnsureSingleType(&timeout, 1, interpreter.NODETYPE_INT, CALL+" :receive-timeout")

	channelMapMu.RLock()
	channel := channelMap[self]
	channelMapMu.RUnlock()

	select {
	case val := <-channel:
		return val
	case <-time.After(time.Duration(timeout.Value.(int)) * time.Millisecond):
		return interpreter.Nothing
	}
}

func doCleanup(p int, r interpreter.Node) {
	channelParentMu.Lock()
	delete(channelParent, p)
	channelParentMu.Unlock()

	channelMapMu.Lock()
	delete(channelMap, p)
	channelMapMu.Unlock()

	watcheeToWatcherMu.RLock()
	for watcher, doSend := range watcheeToWatcher[p] {
		if doSend != 1 {
			continue
		}

		channelMapMu.RLock()
		channel := channelMap[watcher]
		channelMapMu.RUnlock()

		channel <- interpreter.NodeList([]interpreter.Node{
			interpreter.AtomNode("dead"),
			interpreter.IntNode(p),
			r,
		})
	}
	watcheeToWatcherMu.RUnlock()

	watcheeToWatcherMu.RLock()
	watchedByProcess := make([]int, 0)
	for k := range watcheeToWatcher[p] {
		watchedByProcess = append(watchedByProcess, k)
	}
	watcheeToWatcherMu.RUnlock()

	watcherToWatcheeMu.RLock()
	watchingProcess := make([]int, 0)
	for k := range watcherToWatchee[p] {
		watchingProcess = append(watchingProcess, k)
	}
	watcherToWatcheeMu.RUnlock()

	watcherToWatcheeMu.Lock()
	delete(watcherToWatchee, p)
	watcherToWatcheeMu.Unlock()

	watcheeToWatcherMu.Lock()
	delete(watcheeToWatcher, p)
	watcheeToWatcherMu.Unlock()

	watcherToWatcheeMu.Lock()
	for _, process := range watchedByProcess {
		delete(watcherToWatchee[process], p)
	}
	watcherToWatcheeMu.Unlock()

	watcheeToWatcherMu.Lock()
	for _, process := range watchingProcess {
		delete(watcheeToWatcher[process], p)
	}
	watcheeToWatcherMu.Unlock()
}

func doSpawn(arg interpreter.Node, variables *map[string]interpreter.Node) interpreter.Node {
	interpreter.EnsureSingleType(&arg, 1, interpreter.NODETYPE_FN, CALL+" :spawn")

	ctx := make(map[string]interpreter.Node, 0)
	interpreter.CopyVariableState(variables, &ctx)

	//Do global mutex when inserting into chan map
	pid := createPidChannel((*variables)[interpreter.SELF].Value.(int))
	ctx[interpreter.SELF] = interpreter.IntNode(pid)

	go func(p int) {
		defer func() {
			if r := recover(); r != nil {
				if val, ok := r.(string); ok {
					doCleanup(p, interpreter.StringNode(val))
				} else if val, ok := r.(interpreter.Node); ok {
					doCleanup(p, val)
				} else {
					doCleanup(p, interpreter.StringNode(fmt.Sprintf("%v", r)))
				}
			}
		}()

		interpreter.DoVariableCall(parser.Node{
			Type:      0,
			Arguments: []parser.Node{},
			Token:     lexer.Token{},
		}, arg, &ctx)
		panic(interpreter.Nothing)
	}(pid)

	return interpreter.IntNode(pid)
}

func doSleep(duration, mode interpreter.Node) interpreter.Node {
	interpreter.EnsureSingleType(&duration, 1, interpreter.NODETYPE_INT, CALL+" :sleep")
	interpreter.EnsureSingleType(&mode, 2, interpreter.NODETYPE_ATOM, CALL+" :sleep")

	d := time.Duration(int64(duration.Value.(int)))

	switch mode.Value.(string) {
	case "h":
		time.Sleep(d * time.Hour)
	case "min":
		time.Sleep(d * time.Minute)
	case "s":
		time.Sleep(d * time.Second)
	case "ms":
		time.Sleep(d * time.Millisecond)
	default:
		panic(CALL + " :sleep only accepts :h, :min, :s or :ms as second argument!")
	}

	return interpreter.Nothing
}

func doUnwatch(watchee interpreter.Node, self int) interpreter.Node {
	interpreter.EnsureSingleType(&watchee, 1, interpreter.NODETYPE_INT, CALL+" :unwatch")

	w := watchee.Value.(int)
	watcheeToWatcherMu.Lock()
	delete(watcheeToWatcher[w], self)
	watcheeToWatcherMu.Unlock()

	watcherToWatcheeMu.Lock()
	delete(watcherToWatchee[self], w)
	watcherToWatcheeMu.Unlock()

	return interpreter.Nothing
}

// Init Hummus stdlib stub
func Init(variables *map[string]interpreter.Node) {
	initOnce.Do(func() {
		(*variables)[interpreter.SELF] = interpreter.IntNode(createPidChannel(0))
	})
}

// DoSystemCall Hummus stdlib stub
func DoSystemCall(args []interpreter.Node, variables *map[string]interpreter.Node) interpreter.Node {
	mode := args[0].Value.(string)

	switch mode {
	case "die":
		panic(args[1])
	case "watch":
		return doWatch(args[1], variables)
	case "unwatch":
		return doUnwatch(args[1], (*variables)[interpreter.SELF].Value.(int))
	case "send":
		return doSend(args[1], args[2])
	case "receive":
		return doReceive(variables)
	case "receive-until":
		return doReceiveWithTimeout(variables, args[1])
	case "spawn":
		return doSpawn(args[1], variables)
	case "sleep":
		return doSleep(args[1], args[2])
	default:
		panic("Unrecognized mode")
	}
}
