import { buildName, bytes, canaryText, enumName, short, sloText } from "./format";
import { BuildStrategy } from "./gen/bohux/v1/app_pb";

test("enum names read like the CLI's", () => {
  expect(enumName("DEPLOYMENT_STATE_", "DEPLOYMENT_STATE_ROLLED_BACK")).toBe("rolled back");
  expect(enumName("X_", undefined)).toBe("unknown");
});

test("short IDs are the last eight characters", () => {
  expect(short("01a15f47-625d-7cf4-944d-e6a718f0cc79")).toBe("18f0cc79");
});

test("byte counts read like the CLI's", () => {
  expect(bytes(64n * 2n ** 20n)).toBe("64MiB");
  expect(bytes(3 * 2 ** 29)).toBe("1.5GiB");
});

test("build strategies read as sentences", () => {
  expect(buildName(BuildStrategy.UNSPECIFIED)).toBe("its Dockerfile, or Railpack if it has none");
  expect(buildName(BuildStrategy.RAILPACK)).toBe("Railpack");
  expect(buildName(BuildStrategy.DOCKERFILE)).toBe("its Dockerfile");
});

test("a canary reads as its share and bake", () => {
  expect(canaryText({ weightPercent: 10, bake: { seconds: 900n, nanos: 0 } } as never)).toBe("10% for 15m");
  expect(canaryText({ weightPercent: 25, bake: { seconds: 5400n, nanos: 0 } } as never)).toBe("25% for 1h30m");
  expect(canaryText(undefined)).toBe("");
});

test("an SLO reads as its targets, burn rate and watch", () => {
  expect(sloText({ availabilityTarget: 0.999, latency: { thresholdMs: 250, target: 0.95 }, maxBurnRate: 0 } as never))
    .toBe("99.9%, 95% < 250ms, burn 10, watch 10m");
  expect(sloText({ availabilityTarget: 0.99, maxBurnRate: 14.4, watch: { seconds: 90n, nanos: 0 } } as never))
    .toBe("99%, burn 14.4, watch 90s");
  expect(sloText(undefined)).toBe("");
});
