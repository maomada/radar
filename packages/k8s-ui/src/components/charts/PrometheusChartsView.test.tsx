import { describe, expect, it } from "vitest";

import { describePodCoverage } from "./PrometheusChartsView";
import type { PrometheusResourceMetricsResult } from "./PrometheusChartsView";

function result(
  patch: Partial<PrometheusResourceMetricsResult>,
): PrometheusResourceMetricsResult {
  return { unit: "cores", ...patch } as PrometheusResourceMetricsResult;
}

describe("describePodCoverage", () => {
  it("names the pods a chart covers and what it therefore misses", () => {
    expect(describePodCoverage(result({ pods: 3, podsTotal: 3 }))).toBe(
      "3 个当前 Pod；不包含此时间窗口内已被替换的 Pod",
    );
    expect(describePodCoverage(result({ pods: 1, podsTotal: 1 }))).toBe(
      "1 个当前 Pod；不包含此时间窗口内已被替换的 Pod",
    );
  });

  it("says when the cap cut the list, so a short count is not read as the workload", () => {
    expect(describePodCoverage(result({ pods: 50, podsTotal: 120 }))).toBe(
      "共 120 个当前 Pod 中的前 50 个；不包含此时间窗口内已被替换的 Pod",
    );
  });

  it("distinguishes a workload with no pods from a kind that has none to report", () => {
    expect(describePodCoverage(result({ pods: 0 }))).toBe(
      "此工作负载没有正在运行的 Pod，因此没有可绘制的图表",
    );
    // A Node chart carries no pod count at all; it gets no caption rather
    // than a claim about pods.
    expect(describePodCoverage(result({}))).toBeUndefined();
  });
});
