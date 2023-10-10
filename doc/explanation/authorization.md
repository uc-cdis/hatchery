# Authorization

## Global authorization

Hatchery supports global authorization [through the revproxy](https://github.com/uc-cdis/cloud-automation/blob/d842370/kube/services/revproxy/gen3.nginx.conf/hatchery-service.conf#L3-L9). Users need access to the following to launch a workspace:
- Resource: `/workspace`
- Service: `jupyterhub`
- Method: `access`

## Container authorization

In addition to global workspace access, we can control who can launch specific containers: each container can be configured with its own authorization block.
```
"hatchery": {
    "containers": [
        {
            "name": "Jupyter",
            ...
            "authz": {
                "version": 0.1,
                ...
            }
        }
    ],
    ...
}
```

### Container authorization version 0.1

The authorization block consists of a set of rules. "Or" and "and" logics are supported. However, the rules can only be nested up to 1 level.

```
"authz": {
    "version": 0.1,
    "rules": {
        "or": [
            {"resource_paths": ["/workspace/jupyter-container"]},
            {"pay_models": ["Direct Pay", "None"]}
        ]
    }
}
```

Valid `pay_models` values:
- "Direct Pay"
- "STRIDES Credits"
- "STRIDES Grant"
- "None" to allow no pay model (aka blanket billing)

#### Examples of VALID authorization blocks

- Authorize based on access to resource paths only:
```
"rules": { "resource_paths": ["/workspace/abc"] }
```
- Authorize based on pay model type only:
```
"rules": { "pay_models": ["Direct Pay"] }
```
- Authorize based on *both* pay model type *and* access to resource paths (a user can launch this container if they meet *both* requirements):
```
"rules": {
    "and": [
        {"resource_paths": ["/workspace/abc"]},
        {"pay_models": ["Direct Pay"]}
    ]
}
```
- Authorize based on *either* pay model type *or* access to resource paths (a user can launch this container if they meet *either* requirement):
```
"rules": {
    "or": [
        {"resource_paths": ["/workspace/abc"]},
        {"pay_models": ["Direct Pay"]}
    ]
}
```

#### Examples of INVALID authorization blocks

- Empty values:
```
// invalid
"rules": {}
```
```
// invalid
"rules": { "resource_paths": [] }
```
- Multiple keys at the same rule level:
```
// invalid
"rules": {
    "or": [
        {"resource_paths": ["/workspace/abc"]},
        {"pay_models": ["Direct Pay"]}
    ],
    "pay_models": ["Direct Pay"]
}
```
- Nested rules are not supported yet:
```
// invalid
"rules": {
    "or": [
        {"resource_paths": ["/workspace/a"]},
        {
            "and": [
                {"resource_paths": ["/workspace/b"]},
                {"pay_models": ["Direct Pay"]}
            ]
        }
    ]
}
```
