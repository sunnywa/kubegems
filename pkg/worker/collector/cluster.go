package collector

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"kubegems.io/pkg/log"
	"kubegems.io/pkg/service/models"
	"kubegems.io/pkg/utils"
	"kubegems.io/pkg/utils/agents"
	"kubegems.io/pkg/utils/exporter"
)

type ClusterCollector struct {
	clusterUp *prometheus.Desc

	agents *agents.ClientSet
	mutex  sync.Mutex
}

func NewClusterCollector(agents *agents.ClientSet) func(_ *log.Logger) (exporter.Collector, error) {
	return func(_ *log.Logger) (exporter.Collector, error) {
		return &ClusterCollector{
			agents: agents,
			clusterUp: prometheus.NewDesc(
				prometheus.BuildFQName(exporter.GetNamespace(), "cluster", "up"),
				"Gems cluster status",
				[]string{"cluster_name", "api_server", "version"},
				nil,
			),
		}, nil
	}
}

func (c *ClusterCollector) Update(ch chan<- prometheus.Metric) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var clusters []*models.Cluster
	if err := dbinstance.DB().Find(&clusters).Error; err != nil {
		return err
	}

	// TODO: add context
	ctx := context.Background()

	var wg sync.WaitGroup
	for _, cluster := range clusters {
		wg.Add(1)
		go func(clus *models.Cluster) { // 必须把i传进去
			defer wg.Done()

			ishealth := true

			cli, err := c.agents.ClientOf(ctx, clus.ClusterName)
			if err != nil {
				ishealth = false
			}
			if err := cli.Extend().Healthy(ctx); err != nil {
				ishealth = false
			}

			ch <- prometheus.MustNewConstMetric(
				c.clusterUp,
				prometheus.CounterValue,
				utils.BoolToFloat64(&ishealth),
				clus.ClusterName, clus.APIServer, clus.Version,
			)
		}(cluster)
	}
	wg.Wait()
	return nil
}
