package main

import (
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// desktopNotifier shows system notifications through Wails' notification
// service. Permission is asked at the first one, so the system's prompt (macOS)
// appears only when there is something to say. A click on a notification brings
// the window back. Where notifications are not possible, such as a macOS app
// without a bundle, it shows nothing and the tray item and the page still tell
// the user.
type desktopNotifier struct {
	ns   *notifications.NotificationService
	show func()

	once sync.Once
}

func (n *desktopNotifier) send(title, body string) bool {
	n.once.Do(func() {
		n.ns.OnNotificationResponse(func(notifications.NotificationResult) { n.show() })
	})
	if granted, err := n.ns.RequestNotificationAuthorization(); err != nil || !granted {
		log.Printf("sharetoken-gui: notifications not allowed (%v)", err)
		return false
	}
	if err := n.ns.SendNotification(notifications.NotificationOptions{ID: "sharetoken-update", Title: title, Body: body}); err != nil {
		log.Printf("sharetoken-gui: notification: %v", err)
		return false
	}
	return true
}
