import { enumName, short } from "./format";

test("enum names read like the CLI's", () => {
  expect(enumName("DEPLOYMENT_STATE_", "DEPLOYMENT_STATE_ROLLED_BACK")).toBe("rolled back");
  expect(enumName("X_", undefined)).toBe("unknown");
});

test("short IDs are the last eight characters", () => {
  expect(short("01a15f47-625d-7cf4-944d-e6a718f0cc79")).toBe("18f0cc79");
});
