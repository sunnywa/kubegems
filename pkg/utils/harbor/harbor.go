// Copyright 2022 The kubegems.io Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	containerdreference "github.com/containerd/containerd/reference"
	dockerreference "github.com/containerd/containerd/reference/docker"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
	"github.com/goharbor/harbor/src/common"
	harborerrors "github.com/goharbor/harbor/src/lib/errors"
	"github.com/goharbor/harbor/src/pkg/artifact"
	"github.com/goharbor/harbor/src/pkg/label/model"
	"github.com/goharbor/harbor/src/pkg/scan/vuln"
)

const (
	apiVerisonPrefix = "/api/v2.0"
)

const csrfTokenHeader = "X-Harbor-CSRF-Token"

/*
 * 重新定义  AdditionLink  Tag  Label 等结构体的原因为如果从 harbor 引入这些类型，会引入 beego 一大堆东西
 */

// nolint: tagliatelle
type Artifact struct {
	artifact.Artifact
	Tags          []Tag                               `json:"tags"`
	AdditionLinks map[string]AdditionLink             `json:"addition_links"`
	Labels        []Label                             `json:"labels"`
	ScanOverview  map[string]vuln.NativeReportSummary `json:"scan_overview"`
}

// AdditionLink is a link via that the addition can be fetched
type AdditionLink struct {
	HREF     string `json:"href"`
	Absolute bool   `json:"absolute"` // specify the href is an absolute URL or not
}

// Tag is the overall view of tag
// nolint: tagliatelle
type Tag struct {
	ID           int64     `json:"id"`
	RepositoryID int64     `json:"repository_id"`
	ArtifactID   int64     `json:"artifact_id"`
	Name         string    `json:"name"`
	PushTime     time.Time `json:"push_time"`
	PullTime     time.Time `json:"pull_time"`
	Immutable    bool      `json:"immutable"`
	Signed       bool      `json:"signed"`
}

// Label holds information used for a label
// nolint: tagliatelle
type Label struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Color        string    `json:"color"`
	Level        string    `json:"level"`
	Scope        string    `json:"scope"`
	ProjectID    int64     `json:"project_id"`
	CreationTime time.Time `json:"creation_time"`
	UpdateTime   time.Time `json:"update_time"`
	Deleted      bool      `json:"deleted"`
}

type Vulnerabilities map[string]vuln.Report

// 参考OCI规范此段实现 https://github.com/opencontainers/distribution-spec/blob/main/spec.md#determining-support
// 目前大部分(所有)镜像仓库均实现了OCI Distribution 规范，可以使用 /v2 接口进行推断，
// 如果认证成功则返回200则认为实现了OCI且认证成功
func TryLogin(ctx context.Context, registryurl string, username, password string) error {
	// trim http/https prefix
	registryurl = strings.TrimPrefix(registryurl, "http://")
	registryurl = strings.TrimPrefix(registryurl, "https://")
	sys := &types.SystemContext{}
	return docker.CheckAuth(ctx, sys, username, password, registryurl)
}

var ErrNotHarborImage = errors.New("not a harbor suit image")

type HarborAuth struct {
	Username string
	Password string
}

type GetArtifactOptions struct {
	WithTag             bool
	WithScanOverview    bool
	WithLabel           bool
	WithImmutableStatus bool
	WithSignature       bool
}

type Options struct {
	Addr     string `json:"addr,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func NewClient(server string, username, password string) *Client {
	return &Client{OCIDistributionClient: OCIDistributionClient{
		Server:   server,
		Username: username,
		Password: password,
	}}
}

type Client struct {
	OCIDistributionClient
	csrftoken string
}

type ListProjectRepositoriesOptions struct {
	Page int
	Size int
}

// RepoRecord holds the record of an repository in DB, all the infors are from the registry notification event.
// nolint: tagliatelle
type RepoRecord struct {
	RepositoryID int64     `json:"repository_id"`
	Name         string    `json:"name"`
	ProjectID    int64     `json:"project_id"`
	Description  string    `json:"description"`
	PullCount    int64     `json:"pull_count"`
	StarCount    int64     `json:"star_count"`
	CreationTime time.Time `json:"creation_time"`
	UpdateTime   time.Time `json:"update_time"`
}

// GET https://{host}/api/v2.0/projects/{project}/repositories?page_size=15&page=1
func (c *Client) ListProjectRepositories(ctx context.Context, project string, options ListProjectRepositoriesOptions) ([]RepoRecord, error) {
	queries := url.Values{}
	queries.Set("page", strconv.Itoa(options.Page))
	queries.Set("page_size", strconv.Itoa(options.Size))
	path := fmt.Sprintf("/projects/%s/repositories?%s", project, queries.Encode())
	ret := []RepoRecord{}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// GET https://{host}/api/v2.0/projects/{{project_name}}/repositories/{{repository_name}}/artifacts
func (c *Client) ListArtifact(ctx context.Context, image string, options GetArtifactOptions) ([]Artifact, error) {
	project, repository, _, err := c.parseHarborSuitImage(image)
	if err != nil {
		return nil, err
	}
	queries := url.Values{}
	queries.Set("with_tag", strconv.FormatBool(options.WithTag))
	queries.Set("with_scan_overview", strconv.FormatBool(options.WithScanOverview))
	queries.Set("with_label", strconv.FormatBool(options.WithLabel))
	queries.Set("with_signature", strconv.FormatBool(options.WithSignature))
	queries.Set("with_immutable_status", strconv.FormatBool(options.WithImmutableStatus))
	rawquery := queries.Encode()
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts?%s", project, url.PathEscape(repository), rawquery)
	ret := []Artifact{}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// GET https://{host}/api/v2.0/projects/{{project_name}}/repositories/{{repository_name}}/artifacts/{{reference}}?with_scan_overview=true
func (c *Client) GetArtifact(ctx context.Context, image string, options GetArtifactOptions) (*Artifact, error) {
	project, repository, reference, err := c.parseHarborSuitImage(image)
	if err != nil {
		return nil, err
	}

	queries := url.Values{}
	queries.Set("with_scan_overview", strconv.FormatBool(options.WithScanOverview))
	queries.Set("with_label", strconv.FormatBool(options.WithLabel))
	queries.Set("with_signature", strconv.FormatBool(options.WithSignature))
	queries.Set("with_immutable_status", strconv.FormatBool(options.WithImmutableStatus))
	rawquery := queries.Encode()
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts/%s?%s", project, repository, reference, rawquery)
	ret := &Artifact{}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// GET https://{host}/api/v2.0/projects/{project}/repositories/{repository_name}/artifacts/{reference}/additions/vulnerabilities
func (c *Client) GetArtifactVulnerabilities(ctx context.Context, image string) (*Vulnerabilities, error) {
	project, repository, reference, err := c.parseHarborSuitImage(image)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts/%s/additions/vulnerabilities",
		project, repository, reference)

	// https://github.com/goharbor/harbor/blob/c39345da96d887acb47d2b1e7cf1adafca5db1bb/src/server/v2.0/handler/artifact.go#L346
	// harbor 返回的数据结构! fk harbor
	ret := &Vulnerabilities{}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// POST https://{host}/api/v2.0/projects/{{project_name}}/repositories/{{repository_name}}/artifacts/{{reference}}/scan
func (c *Client) ScanArtifact(ctx context.Context, image string) error {
	project, repository, reference, err := c.parseHarborSuitImage(image)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts/%s/scan", project, repository, reference)
	return c.doRequest(ctx, http.MethodPost, path, nil, nil)
}

// nolint: tagliatelle
type SystemInfo struct {
	WithNotary                  bool   `json:"with_notary"`
	AuthMode                    string `json:"auth_mode"`
	RegistryUrl                 string `json:"registry_url"`
	ExternalUrl                 string `json:"external_url"`
	ProjectCreationRestriction  string `json:"project_creation_restriction"`
	SelfRegistration            bool   `json:"self_registration"`
	HasCaRoot                   bool   `json:"has_ca_root"`
	HarborVersion               string `json:"harbor_version"`
	RegistryStorageProviderName string `json:"registry_storage_provider_name"`
	ReadOnly                    bool   `json:"read_only"`
	WithChartmuseum             bool   `json:"with_chartmuseum"`
	NotificationEnable          bool   `json:"notification_enable"`
}

// GET https://{host}/api/v2.0/systeminfo
func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	info := &SystemInfo{}
	if err := c.doRequest(ctx, http.MethodGet, "/systeminfo", nil, info); err != nil {
		return nil, err
	}
	return info, nil
}

func (c *Client) AddArtifactLabelFromKey(ctx context.Context, image string, key, desc string) error {
	labels, err := c.ListGlobalLabels(ctx)
	if err != nil {
		return nil
	}
	for _, label := range labels {
		if label.Name == key {
			return c.AddArtifactLabel(ctx, image, label.ID)
		}
	}
	// 可能是没标签，需要创建该标签
	if err := c.CreateGlobalLabels(ctx, key, desc); err != nil {
		return err
	}
	// 再试一次
	labels, err = c.ListGlobalLabels(ctx)
	if err != nil {
		return nil
	}
	for _, label := range labels {
		if label.Name == key {
			return c.AddArtifactLabel(ctx, image, label.ID)
		}
	}
	// impossible
	// 可能是创建后无法查询到，稍后再试
	return errors.New("unknown err,plaese try again")
}

func (c *Client) DeleteArtifactLabelFromKey(ctx context.Context, image string, key string) error {
	labels, err := c.ListGlobalLabels(ctx)
	if err != nil {
		return nil
	}
	for _, label := range labels {
		if label.Name == key {
			return c.DeleteArtifactLabel(ctx, image, label.ID)
		}
	}
	return errors.New("unknown err,plaese try again")
}

// POST https://{host}/api/v2.0/projects/{project_name}/repositories/{repository_name}/artifacts/{reference}/labels
// {"id":2}
func (c *Client) AddArtifactLabel(ctx context.Context, image string, labelid int64) error {
	project, repository, reference, err := c.parseHarborSuitImage(image)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts/%s/labels", project, repository, reference)
	return c.doRequest(ctx, http.MethodPost, path, model.Label{ID: labelid}, nil)
}

// DELETE  https://{host}/api/v2.0/projects/projects/{{project_name}}/repositories/{{repository_name}}/artifacts/{{reference}}/labels/{label_id}
func (c *Client) DeleteArtifactLabel(ctx context.Context, image string, labelid int64) error {
	project, repository, reference, err := c.parseHarborSuitImage(image)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/repositories/%s/artifacts/%s/labels/%d", project, repository, reference, labelid)
	return c.doRequest(ctx, http.MethodDelete, path, model.Label{ID: labelid}, nil)
}

// POST https://{host}/api/v2.0/projects/{project_name}/repositories/{repository_name}/artifacts/{reference}/labels
func (c *Client) CreateGlobalLabels(ctx context.Context, key, desc string) error {
	label := model.Label{
		Name:        key,
		Description: desc,
		Color:       LabelColorRed,
		Scope:       common.LabelScopeGlobal,
	}
	return c.doRequest(ctx, http.MethodPost, "/labels", label, nil)
}

const LabelColorRed = "#C92100"

func (c *Client) CreateProjectLabels(ctx context.Context, projectid int64, key, desc string) error {
	label := model.Label{
		Name:        key,
		Description: desc,
		Color:       LabelColorRed,
		Scope:       common.LabelScopeProject,
		ProjectID:   int64(projectid),
	}
	return c.doRequest(ctx, http.MethodPost, "/labels", label, nil)
}

// GET https://{host}/api/v2.0/labels?scope=g
func (c *Client) ListGlobalLabels(ctx context.Context) ([]model.Label, error) {
	labels := []model.Label{}
	if err := c.doRequest(ctx, http.MethodGet, "/labels?scope=g", nil, &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

// GET https://{host}/api/v2.0/labels?scope=p&project_id={id}
func (c *Client) ListProjectLabels(ctx context.Context, projectid int) ([]model.Label, error) {
	labels := []model.Label{}
	path := fmt.Sprintf("/labels?scope=p&project_id=%d", projectid)
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

func (c *Client) parseHarborSuitImage(image string) (project, repository, reference string, err error) {
	_, path, name, tag, err := ParseImag(image)
	if err != nil {
		return "", "", "", err
	}
	if path == "" || name == "" || tag == "" {
		return "", "", "", ErrNotHarborImage
	}
	return path, name, tag, nil
}

func (c *Client) doRequest(ctx context.Context, method string, path string, data interface{}, decodeinto interface{}) error {
	var body io.Reader
	switch typed := data.(type) {
	case io.Reader:
		body = typed
	case []byte:
		body = bytes.NewBuffer(typed)
	case nil:
	default:
		bts, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		body = bytes.NewBuffer(bts)
	}

	req, err := http.NewRequest(method, c.Server+apiVerisonPrefix+path, body)
	if err != nil {
		return err
	}
	if method != http.MethodGet {
		// add csrftoken header
		if c.csrftoken == "" {
			if _, err := c.SystemInfo(ctx); err != nil {
				return fmt.Errorf("error in harbor when get csrt token %w", err)
			}
		}
		req.Header.Add(csrfTokenHeader, c.csrftoken)
		// Content-Type: application/json
		// always add json content header
		req.Header.Add("Content-Type", "application/json")
	}
	req.SetBasicAuth(c.Username, c.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode > http.StatusIMUsed {
		errobj := &harborerrors.Error{}
		if err = json.NewDecoder(resp.Body).Decode(errobj); err != nil {
			return err
		}
		return errobj
	}
	// update csrftoken if exist
	if method == http.MethodGet {
		if csrftoken := resp.Header.Get(csrfTokenHeader); csrftoken != "" {
			c.csrftoken = csrftoken
		}
	}
	if decodeinto == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(decodeinto)
}

// ParseImag
// barbor.foo.com/project/artifact:tag -> barbor.foo.com,project,artifact,tag
// barbor.foo.com/project/foo/artifact:tag -> barbor.foo.com,project,foo/artifact,tag
// barbor.foo.com/artifact:tag -> barbor.foo.com,library,artifact,tag
// project/artifact:tag -> docker.io,project,artifact,tag
func ParseImag(image string) (domain, path, name, tag string, err error) {
	named, err := dockerreference.ParseNormalizedNamed(image)
	if err != nil {
		return
	}
	domain = dockerreference.Domain(named)

	fullpath := dockerreference.Path(named)
	const two = 2

	i := strings.Index(fullpath, "/")
	if i != -1 {
		path, name = fullpath[:i], fullpath[i+1:]
	} else {
		path, name = "library", fullpath
	}

	if tagged, ok := named.(dockerreference.Tagged); ok {
		tag = tagged.Tag()
	}
	if tagged, ok := named.(dockerreference.Digested); ok {
		tag = tagged.Digest().String()
	}
	if tag == "" {
		tag = "latest"
	}
	return
}

// https://github.com/containerd/containerd/blob/0396089f79f241df4d8724a0cd31cf58523756ff/reference/reference.go#L84
func SplitImageNameTag(image string) (string, string) {
	spec, err := containerdreference.Parse(image)
	if err != nil {
		// backoff
		spls := strings.Split(image, ":")
		if len(spls) > 1 {
			return spls[0], spls[1]
		}
		return spls[0], ""
	}
	return spec.Locator, spec.Object
}
