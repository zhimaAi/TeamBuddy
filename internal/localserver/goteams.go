package localserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleGoTeamsStatus GoTeams status/login information
func (s *Server) handleGoTeamsStatus(c *gin.Context) {
	status := "not_configured"
	if s.config.CloudConfig != nil && s.config.CloudConfig.APIBaseURL != "" {
		status = "configured"
	}

	// Check login status
	loggedIn := false
	var userInfo interface{}
	if session, ok := getSession(c); ok {
		loggedIn = true
		status = "online"
		userInfo = gin.H{
			"user_id":  session.UserID,
			"username": session.UserName,
			"admin_id": session.AdminID,
		}
	}

	// Check device registration status and JWT (obtained from current account session)
	deviceRegistered := false
	jwtExists := false
	if sess := s.currentSession(); sess != nil {
		if sess.DeviceMgr != nil {
			deviceRegistered = sess.DeviceMgr.IsRegistered()
		}
		if sess.JWTMgr != nil {
			jwtExists = sess.JWTMgr.HasToken()
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            status,
		"api_base_url":      s.apiBaseURL(),
		"client_version":    s.clientVersion(),
		"logged_in":         loggedIn,
		"user":              userInfo,
		"device_registered": deviceRegistered,
		"jwt_exists":        jwtExists,
	})
}

// getSession Gets the session from the context
func getSession(c *gin.Context) (*browserSession, bool) {
	val, exists := c.Get("session")
	if !exists {
		return nil, false
	}
	session, ok := val.(*browserSession)
	return session, ok
}

// apiBaseURL gets the cloud API address
func (s *Server) apiBaseURL() string {
	if client := s.currentCloudClient(); client != nil {
		return client.BaseURL()
	}
	if s.config.CloudConfig != nil {
		return s.config.CloudConfig.APIBaseURL
	}
	return ""
}

// clientVersion gets the client version
func (s *Server) clientVersion() string {
	if s.config.CloudConfig != nil {
		return s.config.CloudConfig.ClientVersion
	}
	return "dev"
}
