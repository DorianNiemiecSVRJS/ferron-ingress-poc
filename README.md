# Ferron Ingress POC

This is a proof of concept for an ingress controller for Kubernetes that utilizes Ferron web server.

## Installation

Apply the Ingress controller deployment using the following command:

```bash
kubectl apply -f deployment.yaml
```

## Demonstration

Create a new file named `hello.yaml` with the following content:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ferron-poc
  labels:
    app: ferron-poc
spec:
  selector:
    matchLabels:
      app: ferron-poc
  template:
    metadata:
      labels:
        app: ferron-poc
    spec:
      containers:
      - name: c1
        image: learnk8s/app:1.0.0
        ports:
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: app
spec:
  type: NodePort
  ports:
    - port: 80
      targetPort: 8080
      nodePort: 31000
  selector:
    app: ferron-poc
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: app
spec:
  ingressClassName: ferron-poc
  rules:
    - http:
        paths:
          - backend:
              service:
                name: app
                port:
                  number: 80
            path: /
            pathType: Prefix
```

And apply the file using the following command:

```bash
kubectl apply -f hello.yaml
```
