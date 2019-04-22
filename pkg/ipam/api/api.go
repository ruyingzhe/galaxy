package api

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"git.code.oa.com/gaiastack/galaxy/pkg/ipam/floatingip"
	"git.code.oa.com/gaiastack/galaxy/pkg/ipam/schedulerplugin/util"
	"git.code.oa.com/gaiastack/galaxy/pkg/utils/database"
	"git.code.oa.com/gaiastack/galaxy/pkg/utils/httputil"
	"git.code.oa.com/gaiastack/galaxy/pkg/utils/nets"
	pageutil "git.code.oa.com/gaiastack/galaxy/pkg/utils/page"
	"github.com/emicklei/go-restful"
	"github.com/golang/glog"
	"k8s.io/client-go/listers/core/v1"
)

type Controller struct {
	ipam, secondIpam floatingip.IPAM
	podLister        v1.PodLister
}

func NewController(ipam, secondIpam floatingip.IPAM, lister v1.PodLister) *Controller {
	return &Controller{
		ipam:       ipam,
		secondIpam: secondIpam,
		podLister:  lister,
	}
}

type FloatingIP struct {
	IP           string `json:"ip"`
	Namespace    string `json:"namespace,omitempty"`
	AppName      string `json:"appName,omitempty"`
	PodName      string `json:"podName,omitempty"`
	PoolName     string `json:"poolName,omitempty"`
	Policy       uint16 `json:"policy"`
	IsDeployment bool   `json:"isDeployment,omitempty"`
	UpdateTime   int64  `json:"updateTime,omitempty"`
	Status       string `json:"status,omitempty"`
	Releasable   bool   `json:"releasable,omitempty"`
	attr         string
}

func (FloatingIP) SwaggerDoc() map[string]string {
	return map[string]string{
		"ip":           "ip",
		"namespace":    "namespace",
		"appName":      "deployment or statefulset name",
		"podName":      "pod name",
		"policy":       "ip release policy",
		"isDeployment": "deployment or statefulset",
		"updateTime":   "last allocate or release time of this ip",
		"status":       "pod status if exists",
		"releasable":   "if the ip is releasable. An ip is releasable if it isn't belong to any pod",
	}
}

type ListIPResp struct {
	pageutil.Page
	Content []FloatingIP `json:"content,omitempty"`
}

func (c *Controller) ListIPs(req *restful.Request, resp *restful.Response) {
	keyword := req.QueryParameter("keyword")
	key := keyword
	fuzzyQuery := true
	if keyword == "" {
		fuzzyQuery = false
		var err error
		poolName := req.QueryParameter("poolName")
		appName := req.QueryParameter("appName")
		podName := req.QueryParameter("podName")
		namespace := req.QueryParameter("namespace")
		isDep := false
		isDepStr := req.QueryParameter("isDeployment")
		if isDepStr != "" {
			isDep, err = strconv.ParseBool(isDepStr)
			if err != nil {
				httputil.BadRequest(resp, fmt.Errorf("invalid isDeployment(bool field): %s", isDepStr))
				return
			}
		}
		key = util.NewKeyObj(isDep, namespace, appName, podName, poolName).KeyInDB
	}
	glog.V(4).Infof("list ips by %s, fuzzyQuery %v", key, fuzzyQuery)
	fips, err := listIPs(key, c.ipam, c.secondIpam, fuzzyQuery)
	if err != nil {
		httputil.InternalError(resp, err)
		return
	}
	sortParam, page, size := pageutil.PagingParams(req)
	sort.Sort(bySortParam{array: fips, lessFunc: sortFunc(sortParam)})
	start, end, pagin := pageutil.Pagination(page, size, len(fips))
	pagedFips := fips[start:end]
	if err := fillReleasableAndStatus(c.podLister, pagedFips); err != nil {
		httputil.InternalError(resp, err)
		return
	}
	resp.WriteEntity(ListIPResp{Page: *pagin, Content: pagedFips}) // nolint: errcheck
}

func fillReleasableAndStatus(lister v1.PodLister, ips []FloatingIP) error {
	for i := range ips {
		ips[i].Releasable = true
		if ips[i].PodName == "" {
			continue
		}
		pod, err := lister.Pods(ips[i].Namespace).Get(ips[i].PodName)
		if err != nil || pod == nil {
			ips[i].Status = "Deleted"
			continue
		}
		ips[i].Status = string(pod.Status.Phase)
		// On public cloud, we can't release exist pod's ip, because we need to call unassign ip first
		// TODO while on private environment, we can
		ips[i].Releasable = false
	}
	return nil
}

type bySortParam struct {
	lessFunc func(a, b int, array []FloatingIP) bool
	array    []FloatingIP
}

func (by bySortParam) Less(a, b int) bool {
	return by.lessFunc(a, b, by.array)
}

func (by bySortParam) Swap(a, b int) {
	by.array[a], by.array[b] = by.array[b], by.array[a]
}

func (by bySortParam) Len() int {
	return len(by.array)
}

func sortFunc(sort string) func(a, b int, array []FloatingIP) bool {
	switch strings.ToLower(sort) {
	case "namespace asc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].Namespace < array[b].Namespace
		}
	case "namespace desc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].Namespace > array[b].Namespace
		}
	case "podname":
		fallthrough
	case "podname asc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].PodName < array[b].PodName
		}
	case "podname desc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].PodName > array[b].PodName
		}
	case "policy":
		fallthrough
	case "policy asc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].Policy < array[b].Policy
		}
	case "policy desc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].Policy > array[b].Policy
		}
	case "ip desc":
		return func(a, b int, array []FloatingIP) bool {
			return array[a].IP > array[b].IP
		}
	case "ip":
		fallthrough
	case "ip asc":
		fallthrough
	default:
		return func(a, b int, array []FloatingIP) bool {
			return array[a].IP < array[b].IP
		}
	}
}

type ReleaseIPReq struct {
	IPs []FloatingIP `json:"ips"`
}

type ReleaseIPResp struct {
	httputil.Resp
	Unreleased []string `json:"unreleased,omitempty"`
}

func (ReleaseIPResp) SwaggerDoc() map[string]string {
	return map[string]string{
		"unreleased": "unreleased ips, have been released or allocated to other pods, or are not within valid range",
	}
}

func (c *Controller) ReleaseIPs(req *restful.Request, resp *restful.Response) {
	var releaseIPReq ReleaseIPReq
	if err := req.ReadEntity(&releaseIPReq); err != nil {
		httputil.BadRequest(resp, err)
		return
	}
	expectIPtoKey := make(map[string]string)
	for i := range releaseIPReq.IPs {
		temp := releaseIPReq.IPs[i]
		ip := net.ParseIP(temp.IP)
		if ip == nil {
			httputil.BadRequest(resp, fmt.Errorf("%q is not a valid ip", temp.IP))
			return
		}
		keyObj := util.NewKeyObj(temp.IsDeployment, temp.Namespace, temp.AppName, temp.PodName, temp.PoolName)
		expectIPtoKey[temp.IP] = keyObj.KeyInDB
	}
	if err := fillReleasableAndStatus(c.podLister, releaseIPReq.IPs); err != nil {
		httputil.BadRequest(resp, err)
		return
	}
	for _, ip := range releaseIPReq.IPs {
		if !ip.Releasable {
			httputil.BadRequest(resp, fmt.Errorf("%s is not releasable", ip.IP))
			return
		}
	}
	_, unreleased, err := batchReleaseIPs(expectIPtoKey, c.ipam, c.secondIpam)
	var unreleasedIP []string
	for ip := range unreleased {
		unreleasedIP = append(unreleasedIP, ip)
	}
	var res *ReleaseIPResp
	if err != nil {
		res = &ReleaseIPResp{Resp: httputil.NewResp(http.StatusInternalServerError, fmt.Sprintf("server error: %v", err))}
	} else if len(unreleasedIP) > 0 {
		res = &ReleaseIPResp{Resp: httputil.NewResp(http.StatusAccepted, fmt.Sprintf("Unreleased ips have been released or allocated to other pods, or are not within valid range"))}
	} else {
		res = &ReleaseIPResp{Resp: httputil.NewResp(http.StatusOK, "")}
	}
	res.Unreleased = unreleasedIP
	resp.WriteHeader(res.Code)
	resp.WriteEntity(res)
}

func listIPs(keyword string, ipam floatingip.IPAM, secondIpam floatingip.IPAM, fuzzyQuery bool) ([]FloatingIP, error) {
	var fips []database.FloatingIP
	var err error
	if fuzzyQuery {
		fips, err = ipam.ByKeyword(keyword)
	} else {
		fips, err = ipam.ByPrefix(keyword)
	}
	if err != nil {
		return nil, err
	}
	resp := transform(fips)
	if secondIpam != nil {
		var secondFips []database.FloatingIP
		if fuzzyQuery {
			secondFips, err = secondIpam.ByKeyword(keyword)
		} else {
			secondFips, err = secondIpam.ByPrefix(keyword)
		}
		if err != nil {
			return resp, err
		}
		resp2 := transform(secondFips)
		resp = append(resp, resp2...)
	}
	return resp, nil
}

func transform(fips []database.FloatingIP) []FloatingIP {
	var res []FloatingIP
	for i := range fips {
		keyObj := util.ParseKey(fips[i].Key)
		res = append(res, FloatingIP{IP: nets.IntToIP(fips[i].IP).String(),
			Namespace:    keyObj.Namespace,
			AppName:      keyObj.AppName,
			PodName:      keyObj.PodName,
			PoolName:     keyObj.PoolName,
			IsDeployment: keyObj.IsDeployment,
			Policy:       fips[i].Policy,
			UpdateTime:   fips[i].UpdatedAt.Unix(),
			attr:         fips[i].Attr})
	}
	return res
}

func batchReleaseIPs(ipToKey map[string]string, ipam floatingip.IPAM, secondIpam floatingip.IPAM) (map[string]string, map[string]string, error) {
	released, unreleased, err := ipam.ReleaseIPs(ipToKey)
	if len(released) > 0 {
		glog.Infof("releaseIPs %v", released)
	}
	if err != nil {
		return released, unreleased, err
	}
	if secondIpam != nil {
		released2, unreleased2, err := secondIpam.ReleaseIPs(unreleased)
		if len(released2) > 0 {
			glog.Infof("releaseIPs in second IPAM %v", released2)
		}
		for k, v := range released2 {
			released[k] = v
		}
		unreleased = unreleased2
		if err != nil {
			if !(strings.Contains(err.Error(), "Table") && strings.Contains(err.Error(), "doesn't exist")) {
				return released, unreleased, err
			}
		}
	}
	return released, unreleased, nil
}
