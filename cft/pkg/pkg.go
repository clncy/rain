// Package pkg provides functionality similar to the AWS CLI cloudformation package command
// but has greater flexibility, allowing content to be included anywhere in a template
//
// To include content into your templates, use any of the following either as YAML tags
// or as one-property objects, much as AWS instrinsic functions are used, e.g. "Fn::Join"
//
// `Include::Literal`: insert the content of the file into the template directly. The file must be in YAML or JSON format.
// `Include::String`: insert the content of the file as a string
// `Include::Base64`: insert the content of the file as a base64 string
// `Include::S3Uri`: uploads the file or directory (zipping it first) to S3 and returns the S3 URI (i.e. `s3://bucket/key`)
// `Include::S3Http`: uploads the file or directory (zipping it first) to S3 and returns the HTTP URI (i.e. `https://bucket.s3.region.amazonaws.com/key`)
// `Include::S3`: Supply an object with the following properties
//    `Path`: path to the file or directory. If a directory is supplied, it will be zipped before uploading to S3
//    `BucketProperty`: Name of returned property that will contain the bucket name
//    `KeyProperty`: Name of returned property that will contain the object key
//    `VersionProperty`: (optional) Name of returned property that will contain the object version
package pkg

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/internal/aws"
	"github.com/aws-cloudformation/rain/internal/aws/s3"
	"gopkg.in/yaml.v3"
)

func zipPath(root string) (string, error) {
	tmpFile, err := ioutil.TempFile(os.TempDir(), "*.zip")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	w := zip.NewWriter(tmpFile)
	defer w.Close()

	err = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		zipPath, err := filepath.Rel(filepath.Dir(root), path)
		if err != nil {
			return err
		}

		zipPath = filepath.ToSlash(zipPath)

		out, err := w.Create(zipPath)
		if err != nil {
			return err
		}

		_, err = io.Copy(out, in)
		return err
	})
	return tmpFile.Name(), err
}

func isDir(path string) bool {
	info, _ := os.Stat(path)

	return info.IsDir()
}

func upload(path string) (string, string, error) {
	var err error

	if isDir(path) {
		// Zip it!
		path, err = zipPath(path)
		if err != nil {
			return "", "", err
		}
	}

	content, err := ioutil.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	bucket := s3.RainBucket(false)

	key, err := s3.Upload(bucket, content)
	if err != nil {
		return "", "", err
	}

	return bucket, key, nil
}

func includeString(n *yaml.Node) error {
	fn := n.Content[1].Value

	if isDir(fn) {
		return fmt.Errorf("Include::String can not include a directory")
	}

	content, err := ioutil.ReadFile(fn)
	if err != nil {
		return err
	}

	n.Encode(strings.TrimSpace(string(content)))

	return nil
}

func includeLiteral(n *yaml.Node) error {
	fn := n.Content[1].Value

	if isDir(fn) {
		return fmt.Errorf("Include::Literal can not include a directory")
	}

	content, err := ioutil.ReadFile(fn)
	if err != nil {
		return err
	}

	err = yaml.Unmarshal(content, n)
	if err != nil {
		return err
	}

	*n = *n.Content[0]

	return nil
}

func includeS3Http(n *yaml.Node) error {
	fn := n.Content[1].Value

	bucket, key, err := upload(fn)
	if err != nil {
		return err
	}

	n.Encode(fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, aws.Config().Region, key))

	return nil
}

func includeS3Uri(n *yaml.Node) error {
	fn := n.Content[1].Value

	bucket, key, err := upload(fn)
	if err != nil {
		return err
	}

	n.Encode(fmt.Sprintf("s3://%s/%s", bucket, key))

	return nil
}

func includeS3(n *yaml.Node) error {
	var props map[string]string

	err := n.Content[1].Decode(&props)
	if err != nil {
		return err
	}

	fn := props["Path"]

	bucket, key, err := upload(fn)
	if err != nil {
		return err
	}

	out := map[string]string{
		props["BucketProperty"]: bucket,
		props["KeyProperty"]:    key,
	}

	n.Encode(out)
	return nil
}

func transform(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode && len(n.Content) == 2 {
		if n.Content[0].Value == "Include::String" && n.Content[1].Kind == yaml.ScalarNode {
			err := includeString(n)
			if err != nil {
				return err
			}
		} else if n.Content[0].Value == "Include::Literal" && n.Content[1].Kind == yaml.ScalarNode {
			err := includeLiteral(n)
			if err != nil {
				return err
			}
		} else if n.Content[0].Value == "Include::S3Http" && n.Content[1].Kind == yaml.ScalarNode {
			err := includeS3Http(n)
			if err != nil {
				return err
			}
		} else if n.Content[0].Value == "Include::S3Uri" && n.Content[1].Kind == yaml.ScalarNode {
			err := includeS3Uri(n)
			if err != nil {
				return err
			}
		} else if n.Content[0].Value == "Include::S3" {
			err := includeS3(n)
			if err != nil {
				return err
			}
		}
	}

	for _, child := range n.Content {
		err := transform(child)
		if err != nil {
			return err
		}
	}

	return nil
}

// Template returns a copy of the template with assets included as per the various `Include::` functions
func Template(t cft.Template) (cft.Template, error) {
	err := transform(&t.Node)
	return t, err
}
