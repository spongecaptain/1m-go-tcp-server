//go:build linux
// +build linux

package main

import (
	"log"
	"net"
	"reflect"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type epoll struct {
	fd int
	// 使用这个 map 的原因在于：从 epoll_wait 上返回的时候，返回的是文件描述符，在 C 中可以直接针对文件描述符来操作 socket
	// 但是在 Go 中，我们是通过 net.Conn 来操作网络 Socket 的，因此需要在 Go 语言层面上使用 map 来做一个额外的映射
	connections map[int]net.Conn // key 为文件描述符，value 为 net.Conn 结构体
	// 所有涉及 connections 上读写的操作都需要利用 lock 进行上锁
	lock *sync.RWMutex // 读写锁
}

func MkEpoll() (*epoll, error) {
	// epoll_create 返回的是文件描述符
	fd, err := unix.EpollCreate1(0)
	if err != nil {
		return nil, err
	}
	// 在 Go 中为文件描述符添加一层封装
	return &epoll{
		fd:          fd,
		lock:        &sync.RWMutex{},
		connections: make(map[int]net.Conn),
	}, nil
}

func (e *epoll) Add(conn net.Conn) error {
	// Extract file descriptor associated with the connection
	fd := socketFD(conn)
	err := unix.EpollCtl(e.fd, syscall.EPOLL_CTL_ADD, fd, &unix.EpollEvent{Events: unix.POLLIN | unix.POLLHUP, Fd: int32(fd)})
	if err != nil {
		return err
	}
	// 写锁
	e.lock.Lock()
	defer e.lock.Unlock()
	// 注册一些 fd -> net.Conn 的映射逻辑
	e.connections[fd] = conn
	if len(e.connections)%100 == 0 {
		log.Printf("total number of connections: %v", len(e.connections))
	}
	return nil
}

func (e *epoll) Remove(conn net.Conn) error {
	fd := socketFD(conn)
	// epoll 中移除管理
	err := unix.EpollCtl(e.fd, syscall.EPOLL_CTL_DEL, fd, nil)
	if err != nil {
		return err
	}
	// 写锁
	e.lock.Lock()
	defer e.lock.Unlock()
	// 删除映射关系
	delete(e.connections, fd)
	if len(e.connections)%100 == 0 {
		log.Printf("total number of connections: %v", len(e.connections))
	}
	return nil
}

func (e *epoll) Wait() ([]net.Conn, error) {
	events := make([]unix.EpollEvent, 100)
retry:
	n, err := unix.EpollWait(e.fd, events, 100)
	if err != nil {
		// 这种错误不致命，继续 wait
		if err == unix.EINTR {
			goto retry
		}
		return nil, err
	}
	// 上读锁
	e.lock.RLock()
	defer e.lock.RUnlock()
	var connections []net.Conn // result to return
	for i := 0; i < n; i++ {
		conn := e.connections[int(events[i].Fd)] // map
		connections = append(connections, conn)
	}
	return connections, nil
}

// socketFD 将 net.Coon 转换为文件描述符
func socketFD(conn net.Conn) int {
	//tls := reflect.TypeOf(conn.UnderlyingConn()) == reflect.TypeOf(&tls.Conn{})
	// Extract the file descriptor associated with the connection
	//connVal := reflect.Indirect(reflect.ValueOf(conn)).FieldByName("conn").Elem()
	// net.Conn 的实现 net.TCPConn 结构体中有一个私有的 conn 字段，
	tcpConn := reflect.Indirect(reflect.ValueOf(conn)).FieldByName("conn")
	//if tls {
	//	tcpConn = reflect.Indirect(tcpConn.Elem())
	//}
	// 类似的逻辑 ...
	fdVal := tcpConn.FieldByName("fd")
	// pdf 的语义是 File descriptor of poll(epoll)
	pfdVal := reflect.Indirect(fdVal).FieldByName("pfd")
	// 反正最后返回的是 Socket 对应的文件描述符
	return int(pfdVal.FieldByName("Sysfd").Int())
}
