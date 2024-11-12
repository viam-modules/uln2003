# [`uln2003` module](https://github.com/viam-modules/uln2003)

This [uln2003 module](https://app.viam.com/module/viam/uln2003) implements a [ULN2003](https://www.ti.com/product/ULN2003A), used for low-current and low-precision applications using the [`rdk:component:motor` API](https://docs.viam.com/appendix/apis/components/motor/). It supports full, half, and quarter stepping with 4096 steps in a rotation in full-step mode.

## Configure your 28byj-48 motor

> [!NOTE]
> Before configuring your motor, you must [create a machine](https://docs.viam.com/cloud/machines/#add-a-new-machine).

Navigate to the [**CONFIGURE** tab](https://docs.viam.com/configure/) of your [machine](https://docs.viam.com/fleet/machines/) in the [Viam app](https://app.viam.com/).
[Add motor / uln2003:28byj-48 to your machine](https://docs.viam.com/configure/#components).

On the new component panel, copy and paste the following attribute template into your motor's attributes field:

```json
{
  "board": "<your-board-name>",
  "pins": {
    "in1": "<pin-number>",
    "in2": "<pin-number>",
    "in3": "<pin-number>",
    "in4": "<pin-number>"
  },
  "ticks_per_rotation": <int>
}
```

### Attributes

The following attributes are available for `viam:uln2003:28byj-48` motors:

| Attribute | Type | Required? | Description |
| --------- | ---- | --------- | ----------  |
| `board` | string | **Required** | `name` of the [board](https://docs.viam.com/components/board/) the motor driver is wired to. |
| `pins` | object | **Required** | A JSON object containing the pin numbers the `in1`, `in2`, `in3`, and `in4` pins of the motor driver are wired to on the [board](https://docs.viam.com/components/board/). |
| `ticks_per_rotation` | int | **Required** | Number of full steps in a rotation. The motor takes 5.625*(1/64)° per step. One full rotation (360°) is 4096 steps. |

Refer to your motor and motor driver data sheets for specifics.

## Example configuration

### `viam:uln2003:28byj-48`
```json
  {
      "name": "<your-uln2003-28byj-48-motor-name>",
      "model": "viam:uln2003:28byj-48",
      "type": "motor",
      "namespace": "rdk",
      "attributes": {
        "board": "example-board",
        "pins": {
          "in1": "11",
          "in2": "12",
          "in3": "13",
          "in4": "15"
        },
        "ticks_per_rotation": 4096
      }
      "depends_on": []
  }
```

### Next Steps
- To test your motor, expand the **TEST** section of its configuration pane or go to the [**CONTROL** tab](https://docs.viam.com/fleet/control/).
- To write code against your motor, use one of the [available SDKs](https://docs.viam.com/sdks/).
- To view examples using a motor component, explore [these tutorials](https://docs.viam.com/tutorials/).
